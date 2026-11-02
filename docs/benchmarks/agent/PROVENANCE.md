# Real local-project slices — provenance

These fixtures are copies of real code from the owner's projects, pinned to the
commits below, with one controlled mutation per task so the archived tests fail
before the agent runs. The oracle solution is the original file. Original
projects are untouched.

| Source | Commit | URL |
|---|---|---|
| CADiPy | `ff62c50` | https://github.com/Zn070515/CADiPy |
| offipy | `546db40` | https://github.com/Zn070515/offipy |
| ChemRealm | `bf7be53` | https://github.com/Zn070515/ChemRealm |
| ArtFlow | `08b47b7` | https://github.com/Zn070515/ArtFlow |

## Tasks and mutations

| Task | Source file | Difficulty | Mutation (fixture) |
|---|---|---|---|
| t14_cadipy_units | `cadipy/domain/units.py` | easy | `sw_m_to_mm` multiplies by 100 instead of 1000 |
| t15_offipy_aesthetic | `offipy/aesthetic.py` | medium | `parse_rgba` defaults missing alpha to 0.0 instead of 1.0 |
| t16_chemrealm_math | `render/src/deterministic-math.ts` | medium | `LOG2_10_HIGH` constant truncated; log10 series stops after 8 terms instead of 16 |
| t17_artflow_business_rules | `common/business_rules.py` | easy | registration-phase guard condition inverted |
| t18_artflow_core_policies | `core/policies.py` | easy | `archived` phase allows `SCORE` |
| t19_artflow_file_policies | `files/policies.py` | medium | unknown purpose silently returns the `other` policy |
| t20_artflow_authority | `common/authority.py` | medium | `authority_write` clears the scope instead of restoring the prior one |
| t21_artflow_delivery | `common/delivery.py` | medium | media-root prefix check drops the path separator |
| t22_artflow_private_package | `scripts/verify_private_test_package.py` | hard | identity-field guard no longer rejects `phone`/`mobile` |
| t23_artflow_questionnaire | `common/private_test_questionnaire.py` | medium | top-10 advancement also grants round 4 |

## Tests

- The tests are archived unittests (or `node:test` for t16) written against the
  real public API of each slice. They are adapted from the projects' own tests
  or reproduce their assertions; they are not a verbatim copy of the upstream
  suites (which depend on Django databases or vitest).
- Every fixture is self-contained: no external checkout or `node_modules` is
  needed.

## Runtime dependencies

- t17–t21 import Django exceptions or settings. Their acceptance command uses
  the ArtFlow virtualenv:
  `C:\Users\16275\Desktop\ArtFlow\.venv\Scripts\python.exe` (Python 3.12,
  Django 6.0.8). This path is machine-specific; a portable setup can install
  Django in any venv and update the acceptance command.
- t16 uses Node 26's built-in TypeScript type stripping and `node:test`.
- All other tasks use the system Python 3.13/3.12 (standard library only).

## Safety

- Fixtures contain no credentials, cookies, personal data or private keys.
- The code slices are public same-owner repositories; the mutation and oracle
  are documented here.
- These are benchmark fixtures, not a redistribution of the projects.
