# Contributing

Thanks for helping. A few ground rules keep the project easy to extend:

- **New game?** Add a YAML file to `templates/`. Copy an existing one and adjust the install method, ports, start and stop commands, config fields, and stats method.
- **New integration (Discord bot, stats site, etc.)?** Build it as a separate service against the public API in `docs/api.md`. If the API is missing something you need, open an issue or PR for the API rather than adding special cases to the core.
- Run `go test ./...` and `npm run build` in `web/` before opening a PR.
- Keep PRs focused on one change.
