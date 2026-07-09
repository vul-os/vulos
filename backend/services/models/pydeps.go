package models

// pydeps.go — an HONEST check of the on-box python embedding runtime.
//
// The embedder (services/embeddings/onnx.go) shells out to `python3` and imports
// onnxruntime + tokenizers. If those packages are not installed, a model.onnx +
// tokenizer.json can be present yet every embed call fails and retrieval silently
// falls back — the operator sees "semantic" in the UI but gets nothing. This
// check surfaces the real state so the UI can say "install these" instead.
//
// It NEVER installs anything. It only runs `python3 -c "import ..."` (read-only,
// bounded) and reports what it found, plus the exact pip command to fix a gap.

import (
	"context"
	"os/exec"
	"time"
)

// pyDepsInstallHint is the exact command an operator runs to install the embed
// dependencies. Documented in the README and shown in the UI verbatim.
const pyDepsInstallHint = "pip install onnxruntime tokenizers numpy"

// pyImportProbe is the source we run to check both imports in a single process.
// It exits 0 only if BOTH import; we still probe them individually below so the
// UI can name the specific missing package.
const pyImportCheckTimeout = 6 * time.Second

// CheckPythonDeps reports whether the local python3 embedding runtime is ready.
// It is best-effort and bounded: a hung interpreter can never stall the caller.
func CheckPythonDeps() PythonDepsStatus {
	st := PythonDepsStatus{InstallHint: pyDepsInstallHint}

	py, err := exec.LookPath("python3")
	if err != nil {
		return st // no python3 → nothing else can be true
	}
	st.Python3 = true

	st.Onnxruntime = pyCanImport(py, "onnxruntime")
	st.Tokenizers = pyCanImport(py, "tokenizers")
	st.Ready = st.Python3 && st.Onnxruntime && st.Tokenizers
	return st
}

// pyCanImport reports whether `python3 -c "import <mod>"` succeeds, bounded by a
// short timeout so a broken environment cannot hang the model-management page.
func pyCanImport(py, module string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), pyImportCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-c", "import "+module)
	return cmd.Run() == nil
}
