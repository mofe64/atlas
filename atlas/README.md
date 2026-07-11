# Atlas Native App

This directory is the installable Atlas desktop application. It is a Tauri v2
application with a React/TypeScript user interface and a small Rust native host.

## Mental model

```text
React + TypeScript (src/)
        │ explicit invoke("command") calls
        ▼
Tauri/Rust native host (src-tauri/)
        │ OS APIs and packaged desktop executable
        │
        └──────── HTTP ────────> atlas-backend (separate Go process)
```

Tauri's Rust code is sometimes called the app's "backend," but it is not the
Atlas product backend. Rust is the trusted native boundary embedded in the
desktop executable. `atlas-backend` is an independently running network service.

The first screen performs two checks deliberately:

- `invoke("runtime_info")` proves that React can call an allow-listed Rust command.
- `GET /healthz` proves that the native app can reach the new Gin service.

## Project map

- `src/main.tsx` mounts React into the webview.
- `src/App.tsx` owns the starter readiness screen and its state transitions.
- `src/api/backend.ts` is the first HTTP client boundary for the Go service.
- `src-tauri/src/main.rs` is the tiny native executable entry point.
- `src-tauri/src/lib.rs` builds Tauri and registers callable Rust commands.
- `src-tauri/tauri.conf.json` controls window, build, identifier, and bundle settings.
- `src-tauri/capabilities/default.json` is the native capability allow-list.

## Run it

Use the repository's Node version (22.13.1), then install dependencies. Rustup
will read `rust-toolchain.toml` and select Rust 1.88.0 for this directory:

```sh
nvm use 22.13.1
rustup show active-toolchain
npm install
```

Start the Go API in another terminal, then run the native application:

```sh
npm run tauri dev
```

`npm run dev` starts only Vite in a browser. That is useful for pure UI work,
but native commands will correctly show as unavailable because there is no Rust host.

To point the app at a different API:

```sh
VITE_ATLAS_API_URL=http://127.0.0.1:8081 npm run tauri dev
```

## Build an installer

```sh
npm run tauri build
```

Tauri first builds the React assets, compiles the Rust host, then produces the
platform bundle under `src-tauri/target/release/bundle/`. Installer signing and
auto-update configuration are intentionally future deployment decisions; a
local unsigned scaffold should not pretend those trust decisions are complete.

## Debugging order

1. If the UI is wrong, inspect the webview console and React code in `src/`.
2. If an `invoke` fails, verify the command name, its Rust signature, and its
   registration in `generate_handler!`.
3. If the API is offline, run `curl http://127.0.0.1:8080/healthz` before
   debugging React. This distinguishes service failure from UI failure.
4. Rust compiler and Tauri runtime messages appear in the terminal that runs
   `npm run tauri dev`.

Keep the native command surface small. Every registered command expands what
untrusted webview code can ask the operating system to do.
