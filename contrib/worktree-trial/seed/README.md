# textstat trial

A small Go CLI with standard-library dependencies only. The baseline counts
LF-delimited lines (a final nonempty partial line also counts), ASCII
whitespace-delimited words, and bytes from stdin.

```sh
go test ./...
printf 'one two\nthree\n' | go run .
# lines=2 words=3 bytes=14
```

The two tasks in `TASKS.md` are intentionally unfinished. Start each task from
the `trial-seed` tag. Do not change the acceptance runner to make a task pass.
