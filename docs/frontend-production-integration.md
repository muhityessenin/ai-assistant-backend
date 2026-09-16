# Подключение frontend к production backend

Документ предназначен для frontend-разработчиков AI Assistants Platform. Он описывает переход с локального API на backend, развёрнутый на cloud-сервере.

## 1. Адреса backend

Production API:

```text
https://ai-assistant-api.pp.ua/api/v1
```

Служебные адреса:

```text
GET https://ai-assistant-api.pp.ua/health
GET https://ai-assistant-api.pp.ua/ready
GET https://ai-assistant-api.pp.ua/docs
GET https://ai-assistant-api.pp.ua/openapi.yaml
```

`/health`, `/ready`, `/docs` и `/openapi.yaml` не входят в `/api/v1`.

Проверка из терминала:

```bash
curl -k https://ai-assistant-api.pp.ua/health
curl -k https://ai-assistant-api.pp.ua/ready
```

Ожидаемые ответы:

```json
{"data":{"status":"ok"},"meta":{}}
```

```json
{"data":{"migration_version":1,"status":"ready"},"meta":{}}
```

## 2. Важное ограничение текущего TLS

Сейчас на API установлен self-signed сертификат для:

```text
DNS: ai-assistant-api.pp.ua
IP: 77.243.81.177
```

Он шифрует соединение, но не подписан публичным центром сертификации. Поэтому:

- `curl` требует флаг `-k`;
- браузер покажет предупреждение о недоверенном сертификате;
- JavaScript не может программно отключить проверку сертификата;
- публичный frontend не должен отключать TLS-проверку;
- каждый разработчик должен вручную добавить сертификат в доверенные на своём устройстве либо открыть API в браузере и принять предупреждение, если браузер это позволяет;
- для публичного production-релиза сертификат необходимо заменить на доверенный сертификат, например Let’s Encrypt.

Запрещено добавлять в frontend обходы TLS-проверки или использовать `NODE_TLS_REJECT_UNAUTHORIZED=0` в production.

## 3. Production-переменные frontend

Для Vite создайте `.env.production`:

```env
VITE_API_BASE_URL=https://ai-assistant-api.pp.ua/api/v1
VITE_APP_NAME=AI Assistants
VITE_APP_ENV=production
```

Переменные `VITE_*` встраиваются в JavaScript во время сборки и являются публичными. В них нельзя помещать:

- `OPENAI_API_KEY`;
- `JWT_SECRET`;
- пароли PostgreSQL;
- bootstrap-пароль;
- любые другие серверные секреты.

После изменения URL frontend требуется пересобрать:

```bash
npm ci
npm run typecheck
npm run test
npm run build
```

Для одноразового production build переменную можно передать непосредственно:

```bash
VITE_API_BASE_URL=https://ai-assistant-api.pp.ua/api/v1 npm run build
```

Проверьте итоговую сборку локально:

```bash
npm run preview
```

## 4. CORS

При прямых запросах браузера backend должен разрешать origin frontend-приложения.

Примеры backend-конфигурации:

```env
CORS_ALLOWED_ORIGINS=https://app.example.com
```

Несколько origins указываются через запятую:

```env
CORS_ALLOWED_ORIGINS=https://app.example.com,https://admin.example.com
```

Для локальной разработки:

```env
CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
```

Origin включает протокол, домен и порт, но не включает путь и завершающий `/`.

Значение `*` разрешает все browser origins и допустимо только как временная диагностика:

```env
CORS_ALLOWED_ORIGINS=*
```

После изменения backend `.env` контейнер нужно пересоздать, чтобы он перечитал окружение.

## 5. Формат ответов

Успешный JSON-ответ имеет envelope:

```json
{
  "data": {},
  "meta": {}
}
```

Для списков `data` является массивом, а `meta` содержит пагинацию:

```json
{
  "data": [],
  "meta": {
    "page": 1,
    "limit": 20,
    "total": 0
  }
}
```

Ошибка:

```json
{
  "error": {
    "code": "SOME_ERROR",
    "message": "Readable message",
    "details": {}
  }
}
```

Frontend должен принимать решения по HTTP status и `error.code`, а не сравнивать только текст `message`.

Частые статусы:

- `400` — некорректный JSON или параметры;
- `401` — отсутствующий, истёкший или недействительный токен;
- `403` — недостаточно прав;
- `404` — объект отсутствует либо скрыт границей организации/доступа;
- `409` — конфликт, например уже существующий email;
- `422` — ошибка валидации;
- `429` — превышен rate limit;
- `500` — внутренняя ошибка;
- `503` — backend или база данных ещё не готовы.

## 6. Авторизация

### Login

```http
POST /api/v1/auth/login
Content-Type: application/json
```

```json
{
  "email": "user@example.com",
  "password": "user-password"
}
```

Ответ содержит пользователя и токены:

```json
{
  "data": {
    "user": {
      "user_id": "uuid",
      "organization_id": "uuid",
      "email": "user@example.com",
      "name": "User",
      "role": "owner"
    },
    "tokens": {
      "access_token": "...",
      "refresh_token": "...",
      "token_type": "Bearer",
      "expires_in": 900
    }
  },
  "meta": {}
}
```

Защищённые запросы используют:

```http
Authorization: Bearer ACCESS_TOKEN
```

### Refresh

```http
POST /api/v1/auth/refresh
Content-Type: application/json
```

```json
{
  "refresh_token": "CURRENT_REFRESH_TOKEN"
}
```

Refresh token ротируется. После успешного refresh frontend обязан заменить и access token, и refresh token. Старый refresh token повторно использовать нельзя.

Рекомендуемая логика клиента:

1. При `401` выполнить один общий refresh-запрос.
2. Параллельные запросы должны ожидать тот же refresh promise.
3. Сохранить новую пару токенов.
4. Повторить исходный запрос один раз.
5. При ошибке refresh очистить сессию и открыть login.

Нельзя запускать отдельный refresh для каждого параллельного `401`: первый refresh инвалидирует старый токен, остальные завершатся ошибкой.

### Logout

```http
POST /api/v1/auth/logout
Authorization: Bearer ACCESS_TOKEN
Content-Type: application/json
```

```json
{
  "refresh_token": "CURRENT_REFRESH_TOKEN"
}
```

Успешный ответ: `204 No Content`. После него frontend удаляет локальные токены.

### Registration

```http
POST /api/v1/auth/register
Content-Type: application/json
```

```json
{
  "organization_name": "Example Company",
  "name": "Owner Name",
  "email": "owner@example.com",
  "password": "at-least-12-characters"
}
```

Регистрация создаёт новую организацию и пользователя с ролью `owner`. При `PUBLIC_REGISTRATION=false` backend возвращает `403`.

## 7. Базовый API client

Пример минимальной функции:

```ts
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL.replace(/\/$/, '')

type ApiErrorBody = {
  error: {
    code: string
    message: string
    details: Record<string, unknown>
  }
}

export async function apiRequest<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, options)

  if (!response.ok) {
    const body = await response.json() as ApiErrorBody
    throw new Error(`${body.error.code}: ${body.error.message}`)
  }

  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
```

В production-клиенте дополнительно должны быть централизованы:

- Bearer token;
- refresh с single-flight блокировкой;
- очистка сессии после неуспешного refresh;
- разбор `{data,meta}` и `{error}`;
- отмена запросов через `AbortSignal`;
- логирование `X-Request-ID` при ошибках.

## 8. SSE-чат

Сообщение отправляется POST-запросом, поэтому стандартный browser `EventSource` не подходит. Используйте `fetch`, `ReadableStream` и SSE parser.

```http
POST /api/v1/conversations/{conversationId}/messages
Authorization: Bearer ACCESS_TOKEN
Accept: text/event-stream
Content-Type: application/json
```

```json
{
  "content": "User message"
}
```

Backend отправляет события:

```text
message_start
sources
content_delta
message_complete
error
```

Пример frame:

```text
event: content_delta
data: {"delta":"часть ответа"}

```

Порядок обычно следующий:

1. `message_start` — UUID создаваемого сообщения;
2. `sources` — найденные RAG-источники, если они есть;
3. несколько `content_delta`;
4. `message_complete` с UUID и usage;
5. либо `error`, если поток завершился ошибкой.

Не предполагайте, что один сетевой chunk равен одному SSE-событию. Один frame может быть разделён между chunk, а один chunk может содержать несколько frames. Декодируйте байты потоковым `TextDecoder` и разделяйте frames по пустой строке.

Nginx для production API уже настроен без response buffering и с увеличенным timeout для SSE.

## 9. Загрузка документов

Поддерживаются:

- PDF;
- DOCX;
- TXT;
- Markdown.

Максимальный размер backend — 50 MB. Nginx допускает тело до 51 MB.

```ts
const body = new FormData()
body.append('file', file)

await fetch(
  `${API_BASE_URL}/knowledge-bases/${knowledgeBaseId}/documents`,
  {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${accessToken}`,
    },
    body,
  },
)
```

Не устанавливайте `Content-Type` вручную для `FormData`: браузер самостоятельно добавляет multipart boundary.

Upload возвращает `202`, а обработка выполняется асинхронно. Frontend должен опрашивать список документов, пока статус равен:

```text
uploaded
processing
```

Финальные статусы:

```text
ready
failed
```

При `failed` показывайте `error_message`.

## 10. Роли frontend

Backend остаётся единственной границей авторизации. Скрытие кнопок во frontend не заменяет server-side проверку.

- `owner`: полное управление организацией, пользователями, ассистентами, знаниями, чатами, feedback и статистикой;
- `admin`: управление ассистентами и знаниями, модерация, создание сотрудников; не может управлять владельцем или создавать owner;
- `employee`: доступные ассистенты, собственные разговоры, чат и feedback.

Frontend должен получить актуальную роль через:

```http
GET /api/v1/me
```

После восстановления токенов при старте приложения сначала вызывайте `/me`, а затем открывайте защищённые экраны.

## 11. Переключение существующего frontend

Рекомендуемая последовательность:

1. Убедиться, что API отвечает через HTTPS.
2. Добавить self-signed сертификат в доверенные на машине разработчика.
3. Создать `.env.production` с production API URL.
4. Убедиться, что origin frontend разрешён backend CORS.
5. Выполнить typecheck, tests и production build.
6. Проверить login и `/me`.
7. Проверить список ассистентов.
8. Создать разговор и проверить постепенный вывод SSE.
9. Загрузить тестовый документ и дождаться `ready`.
10. Проверить logout и повторный вход.

## 12. Диагностика

### `Failed to fetch` / `Network request failed`

Проверить:

- доверен ли self-signed сертификат;
- существует ли DNS-запись домена;
- нет ли CORS-ошибки в DevTools;
- начинается ли API URL с `https://`;
- содержит ли base URL `/api/v1`.

### `403 FORBIDDEN`

Проверить роль пользователя и доступ к ресурсу. Для регистрации проверить `PUBLIC_REGISTRATION`.

### `401 UNAUTHORIZED`

Проверить Bearer header, срок access token и ротацию refresh token. После смены `JWT_SECRET` все ранее выпущенные access tokens становятся недействительными.

### SSE приходит одним большим ответом

Проверить, что запрос идёт через настроенный virtual host и промежуточный CDN/proxy не включает buffering.

### Upload возвращает `413`

Проверить размер файла. Nginx ограничивает тело 51 MB, backend — 50 MB.

### Документ получает `failed`

Проверить `error_message`, наличие `OPENAI_API_KEY`, доступность embedding-модели и backend logs.

## 13. Безопасность

- Не логировать access и refresh tokens.
- Не отправлять серверные секреты во frontend.
- Не хранить секреты в Git.
- Не использовать `curl -k` как модель безопасности browser-приложения.
- Не доверять роли или `organization_id`, присланным frontend: backend получает их из JWT.
- На logout всегда отзывать refresh token через API, а не только очищать local storage.
- Перед публичным запуском заменить self-signed сертификат на сертификат доверенного центра сертификации.

Актуальный машинный контракт находится по адресу:

```text
https://ai-assistant-api.pp.ua/openapi.yaml
```
