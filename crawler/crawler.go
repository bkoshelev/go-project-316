package crawler

import (
	"code/internal/fetcher"
	"code/internal/link"
	"code/internal/page"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

type Options struct {
	URL         string
	Depth       int
	Retries     int
	Delay       time.Duration
	Timeout     time.Duration
	UserAgent   string
	Concurrency int
	IndentJSON  bool
	HTTPClient  fetcher.HTTPClient
}

type Job struct {
	Type        string
	PageOptions page.PageOptions
	LinkOptions link.LinkOptions
}
type Report struct {
	RootURL     string      `json:"root_url" binding:"required"`
	Depth       int         `json:"depth" binding:"required"`
	GeneratedAt string      `json:"generated_at" binding:"required"`
	Pages       []page.Page `json:"pages" binding:"required"`
}

type Queue struct {
	Mu      sync.RWMutex
	M       []Job
	Crawler *Crawler
}

func (q *Queue) Shift() (Job, bool) {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	if len(q.M) > 0 {
		val := q.M[0]
		q.M = q.M[1:]

		return val, true
	}
	return Job{}, false
}

func (q *Queue) Add(value Job) {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	q.M = append(q.M, value)

	select {
	case q.Crawler.Channels.newJobReadyForAnalyze <- struct{}{}:
	default:
	}
}

func (q *Queue) IsEmpty() bool {
	q.Mu.RLock()
	defer q.Mu.RUnlock()

	return len(q.M) == 0
}

type LinksCache struct {
	// PageURLs          []string
	LinkAnalyzeResults link.LinkAnalyzeResult
}

type Cache struct {
	Mu sync.RWMutex
	// brokenLinks map[string][]string
	visitedLinks map[string]struct{}
	linksInPages map[string][]string
	linksData    map[string]link.LinkAnalyzeResult
	pages        map[string]page.Page
}

func (c *Cache) AddLink(newLink link.LinkAnalyzeResult) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	c.linksData[newLink.URL] = newLink

	// pageLinks, ok := c.linksInPages[newLink.PageURL]
	// if !ok {
	// 	c.linksInPages[newLink.PageURL] = []string{newLink.URL}
	// } else {
	// 	pageLinks = append(pageLinks, newLink.URL)
	// 	c.linksInPages[newLink.PageURL] = pageLinks
	// }
}

func (c *Cache) AddLinkToPage(linkURL string, pageURL string) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.visitedLinks[linkURL] = struct{}{}
	pageLinks, ok := c.linksInPages[pageURL]
	if !ok {
		c.linksInPages[pageURL] = []string{linkURL}
	} else {
		pageLinks = append(pageLinks, linkURL)
		c.linksInPages[pageURL] = pageLinks
	}

}

func (c *Cache) isLinkWasVisited(key string) bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	_, ok := c.visitedLinks[key]
	return ok
}

func (c *Cache) AddPage(page page.Page) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	// pageData, ok := c.pages[page.URL]
	// if ok {
	// 	page.Assets = pageData.Assets
	// 	page.BrokenLinks = pageData.BrokenLinks
	// }
	c.pages[page.URL] = page
}

type Channels struct {
	delayFinished         chan struct{}
	timerJob              chan struct{}
	analyzeJobs           chan Job
	newJobReadyForAnalyze chan struct{}
}

type Crawler struct {
	URL              string
	Depth            int
	Retries          int
	Delay            time.Duration
	Timeout          time.Duration
	UserAgent        string
	Concurrency      int
	IndentJSON       bool
	HTTPClient       fetcher.HTTPClient
	HTTPFetch        fetcher.HTTPFetch
	Channels         Channels
	AnalyzeJobsQueue *Queue
	ctx              context.Context
	cancel           context.CancelFunc
	wg               *sync.WaitGroup
	cache            Cache
}

func (c *Crawler) createOutput() Report {
	for _, pageData := range c.cache.pages {
		for _, linkURL := range c.cache.linksInPages[pageData.URL] {

			linkData, ok := c.cache.linksData[linkURL]

			if !ok {
				continue
			}

			switch {
			case linkData.IsBrokenLink:
				pageData.BrokenLinks = append(
					pageData.BrokenLinks, page.BrokenLink(linkData.LinkAnalyzeOutput),
				)
			case linkData.IsAsset:
				pageData.Assets = append(
					pageData.Assets, page.Asset(linkData.AssetAnalyzeOutput),
				)
			}

			c.cache.pages[pageData.URL] = pageData
		}
	}

	output := Report{
		RootURL:     c.URL,
		Depth:       c.Depth,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Pages:       slices.Collect(maps.Values(c.cache.pages)),
	}

	return output
}

func worker(
	c *Crawler,
) {
	for {
		select {
		case newJob := <-c.Channels.analyzeJobs:
			switch newJob.Type {
			case "page":
				pageOpts := newJob.PageOptions

				func() {
					defer c.wg.Done()

					parsedPageURL, err := url.Parse(pageOpts.PageURL)
					if err != nil {
						c.cache.AddPage(page.Page{
							URL:         pageOpts.PageURL,
							Depth:       pageOpts.Depth,
							CustomError: err,
						})
						return
					}

					pageResult := page.AnalyzePage(c.ctx, pageOpts, c.HTTPFetch)
					c.cache.AddPage(pageResult.PageOutput)

					for _, linkOpts := range pageResult.Links {

						parsedLink, err := url.Parse(linkOpts.LinkURL)
						if err != nil {
							continue
						}

						if !link.ValidateLink(parsedLink) {
							continue
						}
						if !parsedLink.IsAbs() {
							parsedLink, err = link.NormalizeLink(linkOpts.LinkURL, linkOpts.PageURL)
							if err != nil {
								continue
							}
						}

						if !c.cache.isLinkWasVisited(parsedLink.String()) {

							c.wg.Add(1)

							c.AnalyzeJobsQueue.Add(
								Job{
									Type:        "link",
									LinkOptions: link.LinkOptions{PageURL: parsedPageURL, LinkURL: parsedLink, Depth: linkOpts.Depth},
								},
							)
						}
						c.cache.AddLinkToPage(parsedLink.String(), linkOpts.PageURL)

					}

				}()
			case "link":
				linkOpts := newJob.LinkOptions

				func() {
					defer c.wg.Done()

					linkResult := link.AnalyzeLink(c.ctx, linkOpts, c.HTTPFetch)

					if linkResult.IsUnsupportedLink {
						return
					}

					if linkResult.IsAsset {
						c.cache.AddLink(linkResult)
						return
					}

					if linkResult.IsBrokenLink {

						c.cache.AddLink(linkResult)
						return
					}

					if linkResult.IsPage {
						if !linkResult.IsExternalHost &&
							linkResult.PageOptions.Depth <= c.Depth {
							c.wg.Add(1)

							c.AnalyzeJobsQueue.Add(
								Job{
									Type:        "page",
									PageOptions: page.PageOptions(linkResult.PageOptions),
								},
							)
						}
					}

				}()
			}
		case <-c.ctx.Done():
			return
		}
	}
}

func createNewCrawler(ctx context.Context, opts Options) *Crawler {
	ctx, cancel := context.WithCancel(ctx)

	crawler := Crawler{
		URL:        opts.URL,
		Retries:    opts.Retries,
		Delay:      opts.Delay,
		UserAgent:  opts.UserAgent,
		IndentJSON: opts.IndentJSON,
		ctx:        ctx,
		cancel:     cancel,
		wg:         &sync.WaitGroup{},
	}

	crawler.AnalyzeJobsQueue = &Queue{
		M:       make([]Job, 0),
		Crawler: &crawler,
	}

	if opts.Depth < 0 {
		crawler.Depth = 1
	} else {
		crawler.Depth = opts.Depth
	}

	if opts.Timeout == 0 {
		crawler.Timeout = time.Second * 15
	} else {
		crawler.Timeout = opts.Timeout
	}

	if opts.Concurrency == 0 {
		crawler.Concurrency = 1
	} else {
		crawler.Concurrency = opts.Concurrency
	}

	if opts.HTTPClient == nil {
		crawler.HTTPClient = &http.Client{}
	} else {
		crawler.HTTPClient = opts.HTTPClient
	}

	crawler.Channels = Channels{
		timerJob:              make(chan struct{}),
		delayFinished:         make(chan struct{}),
		analyzeJobs:           make(chan Job, crawler.Concurrency),
		newJobReadyForAnalyze: make(chan struct{}, 1),
	}

	crawler.cache = Cache{
		linksData:    map[string]link.LinkAnalyzeResult{},
		pages:        map[string]page.Page{},
		visitedLinks: map[string]struct{}{},
		linksInPages: map[string][]string{},
	}

	crawler.HTTPFetch = fetcher.CreateHTTPFetch(fetcher.Options{
		UserAgent:    crawler.UserAgent,
		HTTPClient:   crawler.HTTPClient,
		WaitPauseCh:  crawler.Channels.delayFinished,
		StartPauseCh: crawler.Channels.timerJob,
		Timeout:      crawler.Timeout,
		Retries:      crawler.Retries,
	})
	return &crawler
}

func (c *Crawler) validateOptions() (Report, bool) {
	parsedLink, err := url.Parse(c.URL)
	if err != nil {
		return Report{
			RootURL:     c.URL,
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	if !link.ValidateLink(parsedLink) {
		return Report{
			RootURL:     c.URL,
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}

	normalizedLink, err := link.NormalizeLink(c.URL, c.URL)
	if err != nil {
		return Report{
			RootURL:     c.URL,
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	c.URL = normalizedLink.String()

	return Report{}, true
}

func (c *Crawler) prepareAnalyze() {
	go func() {

		for {
			select {
			case <-c.Channels.newJobReadyForAnalyze:
			case <-c.ctx.Done():
				return
			}

			for {
				newJob, ok := c.AnalyzeJobsQueue.Shift()
				if !ok {
					break
				}

				select {
				case <-c.ctx.Done():
					return
				case c.Channels.analyzeJobs <- newJob:
					continue
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-c.Channels.timerJob:
				timer := time.NewTimer(c.Delay)

				select {
				case <-timer.C:
					select {
					case c.Channels.delayFinished <- struct{}{}:
					case <-c.ctx.Done():
						return
					}
				case <-c.ctx.Done():
					return
				}
			case <-c.ctx.Done():
				return
			}
		}
	}()

}

func (c *Crawler) createWorkers() {
	for w := 0; w < c.Concurrency; w++ {
		go worker(
			c,
		)
	}
}

func (c *Crawler) startAnalyze() {
	// стартуем анализ
	c.wg.Add(1)
	c.cache.AddLinkToPage(c.URL, "")

	c.AnalyzeJobsQueue.Add(
		Job{
			Type: "page",
			PageOptions: page.PageOptions{
				PageURL: c.URL,
				Depth:   0,
			},
		},
	)

	c.Channels.delayFinished <- struct{}{}
}

func (output Report) Format(indentJSON bool) ([]byte, error) {
	var fmtOutput []byte
	var err error

	if indentJSON {
		fmtOutput, err = json.MarshalIndent(output, "", "  ")
	} else {
		fmtOutput, err = json.Marshal(output)
	}

	if err != nil {
		return nil, err
	}
	return fmtOutput, nil
}

func AnalyzeToJSONOutput(ctx context.Context, opts Options) Report {
	crawler := createNewCrawler(ctx, opts)
	output, isValid := crawler.validateOptions()

	if !isValid {
		return output
	}

	crawler.prepareAnalyze()
	crawler.createWorkers()
	crawler.startAnalyze()

	crawler.wg.Wait()
	crawler.cancel()
	return crawler.createOutput()
}

func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	outputJSON := AnalyzeToJSONOutput(ctx, opts)
	return outputJSON.Format(opts.IndentJSON)
}
