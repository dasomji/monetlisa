# monetlisa

Terminal image generation powered by OpenAI subscription auth.

`monetlisa` is a small Go CLI derived from `blitz`. It uses the same OpenAI/Codex subscription login flow, but sends prompts to the Responses API with the `image_generation` tool enabled and writes the generated image to disk.

## Auth

Use your OpenAI/Codex subscription login:

```sh
monetlisa login
```

Open the printed URL, enter the code, and `monetlisa` saves OAuth tokens to:

```sh
~/.monetlisa/auth.json
```

It also falls back to an existing Codex CLI login at:

```sh
~/.codex/auth.json
```

## Usage

```sh
monetlisa "A tiny oil painting of a corgi astronaut on the moon"
monetlisa -o cover.webp "Editorial cover art: a calm robot gardener"
monetlisa --size 1024x1536 --quality high --format webp "Tall art nouveau poster of a fox magician"
monetlisa --image screenshot.png --action edit -o mockup.webp "Modernize this UI while preserving layout"
cat prompt.txt | monetlisa --output images/generated.webp
monetlisa status
```

The command prints the saved image path. Relative output paths resolve from the directory where you call `monetlisa`, not from `~/.monetlisa`.

## Image input and edits

Pass one or more reference images with `--image` / `-i`. Local files are encoded as `input_image` data URLs; HTTP(S) image URLs and `data:image/...` URLs are passed through. Supported local file types are PNG, JPEG, WEBP, and GIF.

```sh
monetlisa --image screenshot.png --action edit -o mockup.webp \
  "Modernize this dashboard while preserving the information architecture"
monetlisa -i before.png,logo.png --image-detail high -o redesign.webp \
  "Create a cleaner SaaS UI using these references"
```

Use `--action edit` to force image editing/reference behavior. The default `--action auto` lets the model decide whether to generate or edit based on the prompt and attached images.

## Output paths

If `-o` / `--output` is omitted, `monetlisa` uses your configured `default-output-path` when set:

```sh
monetlisa config set default-output-path images/latest.webp
```

If neither is set, it writes a timestamped file in the current directory:

```text
monetlisa-YYYYMMDD-HHMMSS.webp
```

## Defaults and config

Defaults:

- model: `gpt-5.5`
- image tool size: `auto`
- quality: `auto`
- format: `webp`
- background: `auto`
- image generation action: `auto`
- input image detail: `high`
- fast mode: enabled (`service_tier: priority`)
- timeout: `10m`

Configure persistent defaults in `~/.monetlisa/config.json`:

```sh
monetlisa config set model gpt-5.5
monetlisa config set default-output-path images/latest.webp
monetlisa config set size 1024x1024
monetlisa config set quality high
monetlisa config set format webp
monetlisa config set background auto
monetlisa config unset default-output-path
```

Supported config keys include: `model`, `codex-home`, `skills-dir`, `prompt`, `default-output-path`, `size`, `quality`, `format`, `background`, `reasoning`, `max-output-tokens`, `timeout`, and `fast`.

## Supported combinations

A parallel sweep of all exposed combinations found that `background=auto` and `background=opaque` work across the supported sizes, qualities, and formats. `background=transparent` is rejected by the current subscription image model (`Transparent background is not supported for this model`), so the CLI now rejects it before making the request.

In that sweep, `webp` produced substantially smaller files than `png` while retaining good quality, so the default output format is `webp`.

## Flags

```text
-model             model name, default gpt-5.5
-o, -output        output image path; relative paths resolve from current directory
-default-output-path fallback output image path when -output is omitted
-size              auto, 1024x1024, 1024x1536, 1536x1024
-quality           auto, low, medium, high
-format            webp, png, jpeg
-background        auto, opaque
-action            auto, generate, edit
-image-detail      auto, low, high, original
-i, -image         input image path, URL, or data URL (repeatable, comma-separated)
-prompt            optional system prompt
-skills-dir        directory of skill markdown prompts
-fast              request Fast mode via service_tier=priority; default true
-timeout           request timeout, default 10m
-codex-home        Codex home containing auth.json
-monetlisa-home    Monetlisa home containing auth.json and config.json
```

Environment variables:

```text
MONETLISA_MODEL
MONETLISA_PROMPT
MONETLISA_DEFAULT_OUTPUT_PATH
MONETLISA_SIZE
MONETLISA_QUALITY
MONETLISA_FORMAT
MONETLISA_BACKGROUND
MONETLISA_SKILLS_DIR
MONETLISA_REASONING_EFFORT
MONETLISA_HOME
CODEX_HOME
```

## Build

```sh
mise x go@1.26.10 -- go test ./...
mise x go@1.26.10 -- go build -o monetlisa ./cmd/monetlisa
```

If Go is installed normally:

```sh
go test ./...
go build -o monetlisa ./cmd/monetlisa
```

## License

MIT
