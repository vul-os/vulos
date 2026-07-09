# On-box embedding models (private-AI RAG)

This package manages the local embedding/RAG model directory (`~/.vulos/models/`)
that powers **semantic** search over your mail — entirely on your own box.

## Retrieval modes (honest)

| Mode       | Requires                              | Quality |
|------------|---------------------------------------|---------|
| `semantic` | a `model.onnx` **and** its real `tokenizer.json` | genuine meaning-based search |
| `degraded` | a `model.onnx` but **no** `tokenizer.json` | reproducible but weak (FNV-hash fallback) |
| `lexical`  | no model installed                    | on-box keyword search (fully sovereign, no model) |

The assistant works in every mode; `semantic` is the best.

## Installing a model (one command / one click)

There are two ways to get to `semantic`:

### 1. Download a curated, pinned model (recommended)

An owner can install the recommended model in **one click**:

- **UI:** Settings → **AI Models** → *Download a recommended model* → **Download**.
- **Onboarding:** the install wizard offers *"Enable private AI search?"* right
  before entering the desktop (optional — skippable; adds the model at install time).
- **API:** `POST /api/models/download {"id":"all-MiniLM-L6-v2"}` (owner-gated).

The download is a **curated, pinned catalog** (see `catalog.go`), not an
arbitrary-URL fetcher:

- the request body carries only a catalog **id**, never a URL (no SSRF surface);
- the only addresses ever fetched are the hardcoded, **https + host-pinned**
  (`huggingface.co`) catalog URLs;
- every artifact is **SHA-256-verified fail-closed** (a mismatch installs nothing),
  size-bounded, content-sniffed (ONNX magic / tokenizer JSON), and installed
  atomically (temp + rename).

The model binary is **fetched on demand and never committed to the repo**.

### 2. Import a model you already have

`POST /api/models/import` (multipart `kind=model|tokenizer`, file `artifact`) —
or Settings → AI Models → *import*. Same validation/atomic guarantees as download,
minus the checksum (you supply the bytes).

## Python dependencies (required to actually run embeddings)

The embedder (`services/embeddings/onnx.go`) shells out to `python3` with
`onnxruntime` + `tokenizers`. **A model can be installed but embeddings will not
run until these are present** — the box then silently falls back. Install them on
the box (never installed automatically):

```
pip install onnxruntime tokenizers numpy
```

`GET /api/models` reports `embeddings.python_deps` (`{python3, onnxruntime,
tokenizers, ready, install_hint}`) so the UI shows an honest "install these"
message instead of a silent degrade. The AI Models panel surfaces this directly.

## Verifying semantic vectors

`services/embeddings/onnx_smoke_test.go` proves the shipped embedder produces real
384-dim vectors (two related sentences score closer than two unrelated ones). It is
gated:

```
VULA_TEST_MODELS_DIR=/path/to/models \
  go test -run RealSemantic ./backend/services/embeddings/
```

(`/path/to/models` must contain a real `model.onnx` + `tokenizer.json`, e.g. after
a `POST /api/models/download`.)
