These test files are used by shuttle's integration and E2E tests.
Do not modify them — tests expect exact content and sizes.

Files:
  small.txt       — 27 bytes, simple text
  medium.txt      — ~1KB, generated text
  large.dat       — ~100KB, generated binary
  unchanged/      — files that should never be modified (baseline)
  changed/        — modified versions of unchanged/* for delta testing
