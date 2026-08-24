package main

import (
	"code/crawler"
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v3"
)

func AnalyzeCLI(customHTTPClient *http.Client) int {
	cmd := &cli.Command{
		Name:            "hexlet-go-crawler",
		Usage:           "analyze a website structure",
		HideHelpCommand: false,
		HideHelp:        false,
		ArgsUsage:       "<url>",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "url",
				Value:     "",
				UsageText: "path to first file",
			},
		},
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "depth",
				Value: 10,
				Usage: "crawl depth",
			},
			&cli.IntFlag{
				Name:  "retries",
				Value: 1,
				Usage: "number of retries for failed requests",
			},
			&cli.StringFlag{
				Name:  "delay",
				Value: "0s",
				Usage: "delay between requests (example: 200ms, 1s)",
			},
			&cli.StringFlag{
				Name:  "timeout",
				Value: "15s",
				Usage: "per-request timeout",
			},
			&cli.IntFlag{
				Name:  "rps",
				Value: 0,
				Usage: "limit requests per second (overrides delay)",
			},
			&cli.StringFlag{
				Name:  "user-agent",
				Usage: "custom user agent",
			},
			&cli.IntFlag{
				Name:  "workers",
				Value: 4,
				Usage: "number of concurrent workers",
			},
		},

		Action: func(ctx context.Context, cmd *cli.Command) error {
			HTTPClient := &http.Client{}

			if customHTTPClient != nil {
				HTTPClient = customHTTPClient
			}

			timeout, err := time.ParseDuration(cmd.String("timeout"))
			if err != nil {
				return fmt.Errorf("invalid timeout value")
			}

			rps := cmd.Int("rps")

			delay, err := time.ParseDuration(cmd.String("delay"))
			if err != nil {
				return fmt.Errorf("invalid delay value")
			}

			if rps > 0 {
				delay = time.Second / time.Duration(rps)
			}

			options := crawler.Options{
				URL:         cmd.StringArg("url"),
				Depth:       cmd.Int("depth"),
				UserAgent:   cmd.String("user-agent"),
				HTTPClient:  HTTPClient,
				Concurrency: cmd.Int("workers"),
				Retries:     cmd.Int("retries"),
				Delay:       delay,
				Timeout:     timeout,
				IndentJSON:  true,
			}

			result, err := crawler.Analyze(ctx, options)

			if err != nil {
				return err
			}

			fmt.Println(string(result))
			return nil
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Println(err)
	}
	return 0
}

func main() {
	os.Exit(AnalyzeCLI(nil))
}
