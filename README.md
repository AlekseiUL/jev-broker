# JEV Broker — tool for agents / инструмент для агентов

[English](#english) · [Русский](#русский) · [My links / Мои ссылки](#links)

## English

JEV Broker is a **local HTTP MCP tool** for agents using Hermes. The agent collects its own evidence, defines named `questions` and chooses `noul` (yes/no), `choice` (options) or `score` (an ordered rubric). It may mix modes in one `evaluate` call. The broker validates input, logs attempt metadata, calls TypeSafe JEV through OpenRouter and returns structured judgments. The agent must then check the original evidence and make its own decision.

This is a **standalone public version of the workflow**, not a copy of a live multi-agent installation. It includes no real profiles, messages, tokens or access rights. It does not read Telegram, search databases, post messages or automatically save agent context. Reading all source texts into the agent first and then sending them to JEV does **not** recover those already spent context tokens. This repository's tests do **not** measure answer-quality gains or token savings.

### Requirements and costs

- macOS or Linux; Go 1.25+, Python 3.9+, and Hermes with stateless MCP 2026-07-28 (`server/discover`) support. Older HTTP MCP clients are rejected before paid calls. Windows is not supported by this release.
- Your **own** OpenRouter account and normal API key. The model call is paid; setup, local tests and MCP `tools/list` are not. The broker allows up to 8 paid requests per call and reserves up to 32 requests per profile per UTC day **in memory**. Restarting it resets that counter. This is not a monetary cap; set an account limit with the provider.
- Run the broker on the same machine as your trusted Hermes profiles. The launcher binds only to `127.0.0.1`. It does not install a background service or configure remote TLS.
- Without `items`, one `evaluate` call makes one paid provider request. With `items`, **each item makes its own sequential paid request** using the same questions. Provider requests are not retried automatically. A timeout or partial response does not prove that no charge occurred.

### Install and verify without a paid request

Clone the repository (Git is required), then run these commands from its root:

```bash
git clone https://github.com/AlekseiUL/jev-broker.git
cd jev-broker
go test ./...
python3 -B -m unittest discover -s scripts -p 'test_*.py'
go build -o bin/jev-broker ./cmd/jev-broker
python3 scripts/setup.py --profile assistant
python3 scripts/run.py --check
```

`setup.py` prompts for the key **without displaying it** and creates private files under `~/.config/jev-broker` by default. Replace `assistant` with your own profile ID; repeat `--profile` for every permitted profile **during the first setup**. An existing private directory is not overwritten. The tests use mocks and do not call the paid model; the initial Go dependency download may require internet.

Run `python3 scripts/run.py` in a separate terminal and **keep it running while agents use JEV**. This package does not set up automatic startup; configure your own process manager if you need the broker after a reboot. The API key stays with the broker; each permitted agent receives its **own** revocable bearer. Find the named profile's actual path with `hermes --profile assistant config env-path`. For a standard named `assistant` profile it is `~/.hermes/profiles/assistant/.env` (not the default profile's `~/.hermes/.env`). Install its generated token there without printing the value:

```bash
python3 scripts/install_profile_env.py --profile assistant --env-file "$HOME/.hermes/profiles/assistant/.env"
```

If the CLI shows a different `.env` path, use that path instead. For another profile, replace the profile ID and check its path first. Then locate the same profile's `config.yaml` with `hermes --profile assistant config path` and add the generated `mcp-assistant.yaml` snippet, merging under a single `mcp_servers:` key. The snippet contains only `${JEV_BROKER_TOKEN_ASSISTANT}`, not its value. Back up the config before editing. Do not auto-install this into every profile.

Reload that profile's MCP connection (or start a new session) and verify its **actual** tool list shows `mcp__jev_broker__evaluate` with `state`, `items`, `questions`. The prefix depends on the MCP server name. A successful `tools/list` does not call OpenRouter. The [agent skill](skills/jev-broker/SKILL.md) teaches when to use the tool but **does not** grant access by itself. Only after checking access and data rights should you separately authorize a synthetic paid test. See the [installation and rollback guide](docs/install-and-verify.md) and [agent guide](AGENT_GUIDE.md).

### Security and limitations

Do not put the OpenRouter key in chats, commands, Git or Hermes profile YAML. Never send credentials, session files or other people's private material to JEV without the necessary rights. The broker's credential check is **best-effort**, not a guarantee. File permissions (`0700/0600`) isolate files from other OS users, **not** from agents with shell/file access under the same OS account. Untrusted profiles require separate OS users or machines with an independently secured connection.

The broker does not impose a monetary budget or decide which data is safe to share. It writes a private audit log of attempt time, profile ID, event, modes and known cost. Unknown or failed calls may still have been charged. With `items`, shared `state` is sent again for each item; the aggregate outbound request JSON is limited to 4 MiB. A JEV probability is not a fact, permission or approval to publish. If a call fails or the outcome is unknown, check the source and **do not blindly retry** a potentially paid request. To disconnect one profile, remove its MCP config and environment variable; this alone does not revoke a stolen bearer. Follow the [revocation instructions](docs/install-and-verify.md#отзыв-доступа-конкретного-профиля). Before another release, use the [release checklist](docs/release-checklist.md).

### Credits and disclosure

This adapts the MIT-licensed [System One Connector](https://github.com/itsmostafa/system-one-connector); see [NOTICE](NOTICE) and [LICENSE](LICENSE). It uses the [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk), connects to [Hermes Agent](https://hermes-agent.nousresearch.com/docs/) and calls TypeSafe JEV via the [OpenRouter Decisions API](https://openrouter.ai/docs/guides/community/jev-tutorial). You provide and pay for your own OpenRouter access. No affiliation with, or endorsement by, those projects is implied. AI-assisted tooling was used during development and editing. Follow the rules of each data source and the rights of the people whose material you process; a technical integration does not waive them.

## Русский

Один локальный MCP-инструмент `evaluate`: агент сам выбирает материал, вопросы и режимы JEV (`noul`, `choice`, `score`). В MCP-конфигурацию агентов ключ OpenRouter не попадает; каждому профилю Hermes выдаётся свой отзывной токен. Java не нужна: сервер написан на Go, вспомогательная установка — на Python.

**Зачем он нужен:** агент получает материал своим обычным способом, формулирует конкретный вопрос и передаёт его Broker. Broker проверяет запрос, записывает метаданные попытки, отправляет его в модель JEV через OpenRouter и возвращает короткую оценку. Агент сверяет её с исходным материалом и сам решает, что делать дальше. Режимы можно смешивать в одном вызове; каждый `items`-элемент — отдельный платный запрос. Без `items` один вызов — один платный запрос. Автоматических повторов и встроенного денежного лимита нет.

Это переносимый публичный вариант **подхода** с общим инструментом для агентов, а не экспорт работающей внутренней установки. В нём нет действующих профилей, переписок, токенов или прав доступа. JEV оценивает только то, что агент явно передал: сам не читает Telegram, файлы или базы и не отправляет сообщения. Скачивание пакета не подключает его к серверу автора или к чужому аккаунту OpenRouter.

## Что понадобится

- **macOS или Linux** (Windows этим выпуском не поддерживается), Go **1.25+**, Python **3.9+**, Hermes с поддержкой stateless MCP 2026-07-28 (`server/discover`); старый HTTP MCP получает отказ до платного вызова.
- Собственный аккаунт OpenRouter и обычный API-ключ, созданный на [странице ключей OpenRouter](https://openrouter.ai/settings/keys). Management API key здесь не нужен.
- Компьютер, где работают Broker и профили Hermes. Этот вариант слушает только `127.0.0.1`; для агентов на других машинах понадобится отдельная защищённая сетевая архитектура, которой этот пакет не настраивает.

Ключ OpenRouter вводится только в скрытый интерактивный запрос `setup.py`. Не передавайте его агенту в чате, не вставляйте в команду терминала, `config.yaml`, Git или Telegram. Настройка не делает платных вызовов. Вызов ограничен 8 платными запросами, профиль — 32 зарезервированными запросами за день UTC, **пока сервер работает**. Перезапуск обнуляет счётчик: это не денежный лимит. Владелец ключа может установить лимит у OpenRouter.

**Граница защиты:** права файлов `0700/0600` закрывают их от других пользователей ОС, но не от агентов, которым доступна произвольная оболочка или чтение файлов **под тем же системным пользователем**. Такие агенты могут прочитать ключ Broker и токены соседних профилей. Если профили не доверены или имеют свободный доступ к shell/filesystem, не считайте эту установку изоляцией секретов: запускайте Broker и профили под разными пользователями ОС (либо на разных машинах с отдельно защищённым соединением) и передавайте каждому только его токен. Инструкция ниже рассчитана на доверенные профили одного пользователя; разграничение на уровне MCP не заменяет изоляцию ОС.

## Быстрый старт на чистой машине

Скопируйте репозиторий и перейдите в его папку (нужен установленный Git). Укажите **свои** имена профилей Hermes; здесь `assistant` и `research` — примеры.

```bash
git clone https://github.com/AlekseiUL/jev-broker.git
cd jev-broker
go test ./...
python3 -B -m unittest discover -s scripts -p 'test_*.py'
go build -o bin/jev-broker ./cmd/jev-broker
python3 scripts/setup.py --profile assistant --profile research
python3 scripts/run.py --check
```

`setup.py` один раз спросит ключ без вывода символов и создаст отдельную приватную папку (по умолчанию `~/.config/jev-broker`): ключ, реестр хешей токенов, журнал, приватный `.env`-фрагмент и YAML-фрагмент для каждого профиля. Файлы имеют права `0600`, папка — `0700`. Если папка уже существует, скрипт **ничего не перезапишет**. Первоначальное скачивание зависимостей Go может требовать интернет; тесты не вызывают платный JEV.

Запустите сервер в отдельном терминале:

```bash
python3 scripts/run.py
```

Команда держит сервер на `http://127.0.0.1:8768/mcp` до `Ctrl-C`. Ключ из приватного файла попадает только в окружение процесса Broker, не в аргументы команды и не в профиль Hermes. Для постоянной работы после проверки настройте собственный менеджер процессов; пакет не устанавливает системную службу автоматически.

## Подключение профиля Hermes

Для **каждого** разрешённого профиля повторите два шага. Не копируйте токен одного профиля другому.

1. Определите активный `.env` **именно этого** профиля командой `hermes --profile assistant config env-path`. У стандартного именованного профиля `assistant` это `~/.hermes/profiles/assistant/.env`, а у профиля по умолчанию — `~/.hermes/.env`; если команда показывает иной путь, используйте **его**. Положите туда токен скрыто, без вывода его значения в терминал. Пример для именованного `assistant`:

   ```bash
   python3 scripts/install_profile_env.py --profile assistant --env-file "$HOME/.hermes/profiles/assistant/.env"
   ```

   Для другого профиля замените `assistant` и путь к его `.env`; скрипт сам сверит этот путь с `hermes --profile ИМЯ config env-path` и откажется писать по иному адресу. При изменении существующего `.env` резервная копия остаётся **рядом с ним** и содержит прежние секреты; если токен уже совпадает, новая копия не создаётся. Сохраняйте и отзывайте эти копии как секретные файлы. Исходный фрагмент остаётся в приватной папке Broker.

2. Узнайте путь к `config.yaml` того же профиля через `hermes --profile assistant config path` и добавьте блок `jev_broker` из приватного `mcp-assistant.yaml`. Если `mcp_servers:` уже есть, добавляйте только вложенный `jev_broker`, не создавайте второй верхнеуровневый ключ. Пример без секрета:

   ```yaml
   mcp_servers:
     jev_broker:
       url: "http://127.0.0.1:8768/mcp"
       protocol: stateless
       headers:
         Authorization: "Bearer ${JEV_BROKER_TOKEN_ASSISTANT}"
         MCP-Protocol-Version: "2026-07-28"
       tools:
         include: [evaluate]
   ```

   Название переменной для другого профиля записано в его `mcp-<profile>.yaml`. Сам токен в YAML **не** вставляйте. Перед правкой сохраните приватную резервную копию `config.yaml`. Это ручной шаг: установщик не трогает работающий Hermes.

Hermes [поддерживает HTTP MCP, `headers`, `tools.include` и подстановку `${VAR}` из секретного окружения профиля](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp). Нужны **оба** поля протокола из примера выше: без них старый handshake не сможет запустить защищённый от осиротевших платных вызовов Broker. Запустите новый сеанс Hermes или используйте `/reload-mcp` в нужном сеансе. Проверьте реальный список инструментов: должен появиться **`mcp__jev_broker__evaluate`** (или эквивалентное имя, которое показывает ваша версия Hermes) со входами `state`, `items`, `questions`. Проверка `tools/list` не отправляет запрос в OpenRouter и ничего не стоит. Если инструмент не виден, не начинайте платный тест — см. [диагностику](docs/install-and-verify.md).

Только после успешного `tools/list` **отдельно** разрешите один синтетический живой вызов по примеру из [гайда агенту](AGENT_GUIDE.md). Он уже платный. Никакой вызов к модели не запускается автоматически при установке.

## Как дать каждому агенту общую инструкцию

В репозитории есть готовый [Hermes-скилл](skills/jev-broker/SKILL.md). Он объясняет агенту, **когда** JEV полезен и как самому составить вопросы всех трёх режимов. Установите его в каждый профиль, которому уже выдали MCP-доступ. Для именованного профиля `assistant` при отсутствии старого скилла с таким именем:

```bash
mkdir -p "$HOME/.hermes/profiles/assistant/skills/jev-broker"
cp skills/jev-broker/SKILL.md "$HOME/.hermes/profiles/assistant/skills/jev-broker/SKILL.md"
```

Для профиля по умолчанию используйте `~/.hermes/skills/jev-broker/`; для остальных замените `assistant` на их имя. Если файл уже есть, сначала изучите и сохраните его — не перезаписывайте вслепую. Начните новый сеанс и проверьте отдельно: скилл появился в `/skills`, а MCP-инструмент — в фактическом списке инструментов. Одно **не** доказывает другое. [Hermes загружает скиллы по мере надобности](https://hermes-agent.nousresearch.com/docs/guides/work-with-skills), поэтому полная инструкция не засоряет каждый запрос.

В опубликованном репозитории скилл можно установить по прямому адресу: `hermes skills install https://raw.githubusercontent.com/AlekseiUL/jev-broker/main/skills/jev-broker/SKILL.md`. Делайте это только для выбранного профиля Hermes после проверки адреса и содержимого скилла; сама установка скилла не подключает MCP и не даёт доступ к JEV.

## Агенту и владельцу

- [AGENT_GUIDE.md](AGENT_GUIDE.md) — когда вызывать JEV, как самому составить `state`/`items`/`questions`, три режима и пример для Telegram.
- [Установка, проверка, откат](docs/install-and-verify.md) — точные признаки успеха, ошибки подключения, обновление профилей и восстановление.
- [Чеклист публикации](docs/release-checklist.md) — что можно выкладывать на GitHub и как проверить отсутствие секретов.

JEV — вероятностный помощник для небольших решений, не источник фактов и не разрешение на действие. Экономия контекста возможна только если отбор сделан **до** чтения агентом всех полных текстов; прирост качества ответов и экономия токенов тестами этого репозитория **не измерены**. Отправляйте собственные или разрешённые к обработке данные; никогда не отправляйте ключи, пароли и файлы сессий.

## Лицензия и использованные компоненты

[System One Connector](https://github.com/itsmostafa/system-one-connector) — исходный MIT-лицензированный проект; автор и исходная лицензия указаны в [NOTICE](NOTICE) и [LICENSE](LICENSE). В этой версии используется [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) для локального HTTP MCP, [Hermes Agent](https://hermes-agent.nousresearch.com/docs/) как клиент и модель TypeSafe JEV через [OpenRouter Decisions API](https://openrouter.ai/docs/guides/community/jev-tutorial). Для работы нужен **ваш** аккаунт и ключ OpenRouter; вызовы модели платные. Это независимый пакет, а не официальный продукт или рекомендация со стороны упомянутых проектов.

Разработка и редактура проходили с помощью AI-ассистента. Тесты и проверка публичных файлов не означают гарантию качества ответов JEV или разрешение передавать чужие данные. Правила источника данных и права людей сохраняются независимо от способа подключения модели.

## Links

My links / Мои ссылки:

- [GitHub — other open-source projects / другие открытые проекты](https://github.com/AlekseiUL)
- [YouTube — videos about agents and tools / видео об агентах и инструментах](https://youtube.com/@alekseiulianov)
- [Telegram SPRUT_AI — public notes / публичные заметки](https://t.me/Sprut_AI)
- [Telegram-чат — discussion / обсуждение](https://t.me/+eH-qNIDmud8zNDZi)
- [AI ОПЕРАЦИОНКА — paid projects and guides / платные проекты и разборы](https://t.me/tribute/app?startapp=sJyg)
