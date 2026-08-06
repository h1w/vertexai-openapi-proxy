# Vertex Native Model Gateway — дизайн

## Цель

Заменить статический список из двух Gemini-моделей динамическим обнаружением Google publisher models и сделать proxy пригодным для всех поддерживаемых inference-операций Vertex AI, а не только OpenAI Chat Completions.

## Границы

Proxy сохраняет OpenAI-совместимый `/v1` для Open WebUI. Для Vertex API, не имеющих эквивалента в OpenAI, добавляется отдельная нативная поверхность. Управление ресурсами Vertex — создание, удаление, deployment, IAM, операции проекта — не проксируются.

## Модельный каталог

`GET /vertex/v1/models` получает модели из Vertex Model Garden через `GET v1beta1/publishers/google/models`, передавая `pageToken` до исчерпания страниц. Ответ кэшируется с TTL, чтобы не выполнять сетевой запрос на каждый вызов UI. Идентификатор из `publishers/google/models/<model>` преобразуется в OpenAI-ID `google/<model>`.

`GET /v1/models` использует тот же полный каталог, чтобы Open WebUI видел все Google publisher models, доступные через API каталог. OpenAI `Model` schema не содержит capability metadata; поэтому Open WebUI может показать модель, которая не поддерживает Chat Completions. Такие модели вызываются через нативный `/vertex/v1` с соответствующей inference-операцией, а попытка вызвать их через `/v1/chat/completions` получает исходную ошибку Vertex.

Vertex API не предоставляет гарантированный список моделей, разрешённых конкретному Free Trial account. Каталог определяет существующие Google publisher models; фактическая доступность остаётся источником истины при inference-вызове и может зависеть от региона, quota, launch stage и allowlist.

## Нативная inference-поверхность

`POST /vertex/v1/models/{publisher}/{model}:{action}` принимает нативное тело Vertex и направляет запрос только в:

`/v1/projects/{VERTEXAI_PROJECT}/locations/{VERTEXAI_LOCATION}/publishers/{publisher}/models/{model}:{action}`

Разрешённый whitelist действий: `generateContent`, `streamGenerateContent`, `embedContent`, `predict`, `rawPredict`, `streamRawPredict`, `serverStreamingPredict`, `predictLongRunning` и `fetchPredictOperation`. Другие методы, publishers и paths возвращают ошибку до обращения к Google.

Ответы, статусы, content type и streaming передаются без преобразования. Токен ADC добавляется proxy так же, как для текущего OpenAI upstream. Ошибки получения токена и обращения к Vertex возвращаются клиенту без секретов и логируются структурированно.

## Аутентификация клиентов

`VERTEXAI_PROXY_API_KEY` обязателен. Все `/v1` и `/vertex/v1` запросы требуют `Authorization: Bearer <key>`, с постоянным сравнением секретов. Docker Compose передаёт ключ в proxy и использует его как `OPENAI_API_KEY` для Open WebUI. Это обязательно, поскольку сервис опубликован в домашней сети и выполняет запросы от имени ADC владельца.

## Проверка

Юнит-тесты покрывают: авторизацию, pagination и TTL cache каталога, преобразование model ID, передачу полного каталога в `/v1/models`, блокировку путей/методов вне whitelist и корректную подстановку project/location/publisher/model в Vertex URL. Интеграционная проверка с ADC подтверждает, что `/vertex/v1/models` возвращает каталог и что один разрешённый inference-запрос проходит с реальным токеном. Docker Compose проверяется на передачу ключа без его вывода в логи.
