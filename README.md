### Hexlet tests and linter status:

[![Actions Status](https://github.com/bkoshelev/go-project-316/actions/workflows/hexlet-check.yml/badge.svg)](https://github.com/bkoshelev/go-project-316/actions)

# Пример использования

```bash
bin/hexlet-go-crawler https://example.com

# тонкая настройка обхода
bin/hexlet-go-crawler --depth 2 --workers 8 https://example.test
```

## Параметры

- `-depth <int>` - глубина анализа домена (0 - будет произведен анализ только переданного URL)

## Команды

- `make build` — собирает CLI-приложение в файл `bin/hexlet-go-crawler`.
- `make test` — запускает все тесты проекта.
- `make run URL=https://example.com` — запускает приложение для указанного URL без предварительной сборки.

## Основные функции:

- [x] Ограничение глубины обхода, задержки между запросами и пользовательский User-Agent
- [ ] Повторные попытки запросов и пул воркеров для ускорения краулинга
- [x] Сбор SEO-метрик: title, meta description и наличие h1
- [x] Проверка внутренних и внешних ссылок с фиксацией битых URL
- [ ] Инвентаризация статических ассетов (CSS/JS/изображения) с указанием размера и статуса
- [ ] Экспорт результата в JSON, пригодный для последующей аналитики
