# Ollama smoke probe

Local-only smoke test for a very small model. It is **not** a product model
provider and it never executes an action: it compiles a BountyGrid reliability
context, asks the model for one proposal, and runs `RankProposals` on the
result.

## Model

`qwen2.5:0.5b` (Q4_K_M, ~491 MB) is a useful ultra-small baseline: it follows
JSON shape most of the time but is not reliable for factual control-plane
decisions.

Download from ModelScope (fast in mainland China; verify the SHA-256 from the
ModelScope file listing):

```bash
curl -L --fail -o qwen2.5-0.5b-instruct-q4_k_m.gguf \
  "https://modelscope.cn/models/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/master/qwen2.5-0.5b-instruct-q4_k_m.gguf"
sha256sum qwen2.5-0.5b-instruct-q4_k_m.gguf
# 74a4da8c9fdbcd15bd1f6d01d621410d31c6fc00986f5eb687824e7b93d7a9db
```

Copy this directory's `Modelfile` next to the GGUF so the relative `FROM` path
resolves, then:

```bash
cd /path/to/model-dir
ollama create qwen25-0.5b-local -f Modelfile
ollama list
```

## Run

Direct API smoke (three BountyGrid-shaped prompts, UTF-8 JSON report):

```bash
python -I probes/ollama_smoke/smoke.py qwen25-0.5b-local
```

Reliability pipeline smoke (ContextCompiler → Ollama → RankProposals):

```bash
go run ./probes/ollama_smoke -model qwen25-0.5b-local
```

## What the smoke showed

On an RTX 5060 Laptop (8 GB), Q4_K_M:

- first load ~5.7 s; subsequent calls ~0.1–0.7 s;
- ~300–350 output tok/s;
- a 1,168-rune Chinese/JSON reliability context was 515 actual prompt tokens;
  the conservative 1-token-per-rune estimate reports 1,168 (~2.3x);
- the model produced valid JSON and a structurally valid `READ_SCOPE`
  proposal, but its eligibility answer was wrong (`eligible=true` while the
  compliance exam was not completed), and it sometimes returns an object
  instead of the requested array or copies `|human_only=false` from the
  context into the `action` field.

Conclusion: useful as a **translation/formatting** baseline, not as an
authority. The Go control layer must keep filtering and deciding; this is
exactly what `RankProposals` and the Action Gateway are for.
