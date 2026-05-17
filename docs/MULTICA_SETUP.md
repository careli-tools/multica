# Multica Setup Notes

Operational notes for the Multica monorepo. Anything that needs to live alongside the code (not on the docs site) goes here.

## Attachments / Excalidraw Preview

`.excalidraw` (and `.excalidraw.json`) files attached to an issue render an inline SVG preview underneath the description editor. This is the viewer-only stage of the Excalidraw integration (CAR-707, Stufe 1) — the editor itself ships in a later stage.

- The preview component lives in `packages/views/issues/components/excalidraw-preview.tsx` and is wired into `IssueDetail` for the `excalidrawAttachments` slice of `issueAttachments`.
- Rendering uses a dynamic `import("@excalidraw/excalidraw")` and only calls `exportToSvg`. The full editor never lands on the initial bundle.
- Light/dark mode is observed on `document.documentElement` and forwarded to `exportToSvg` via `exportWithDarkMode`.
- Diagrams taller than 600px collapse to a scrollable preview with a click-to-expand lightbox.
- Malformed JSON, oversized files (> 16 MB), or network failures fall back to a "Couldn't render — open file" link.

### Detection rules

A file is treated as an Excalidraw scene when its filename ends with `.excalidraw` or `.excalidraw.json` (case-insensitive). Content-type is not used because uploads from the official editor frequently report `application/json`.

### Bundle impact

`@excalidraw/excalidraw` is added to `pnpm-workspace.yaml`'s catalog and consumed by `packages/views` only. The web app declares it under `transpilePackages` in `apps/web/next.config.ts` (the package ships as ESM-only since v0.18). The desktop renderer transpiles `@multica/views` through `electron-vite` and inherits the dependency.

### Adding new previewable scene fields

The component only relies on `type === "excalidraw"` and `Array.isArray(elements)`. If a future scene version restructures those, update `isValidExcalidrawScene` and add a regression case in `excalidraw-preview.test.tsx`.
