// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

// Package mvpack reads, writes, signs and verifies .mvpack files, the portable
// pack format MangaVeyra servers and the World hub use to exchange characters,
// style packs, templates, layouts, series listings and manifest-only plugins.
//
// A pack is a ZIP container with manifest.json at its root, an optional
// signature.json next to it, and payload files drawn from a strict allow-list
// (PNG, JPEG and WebP images, .safetensors weights and JSON documents, plus
// font files in plugin packs). The format is specified in SPEC.md next to this
// file and the manifest is described by schema/manifest.schema.json.
//
// The package only depends on the Go standard library so anyone can vendor it,
// and it is MIT licensed (see sdk/LICENSE) so that tools outside the AGPL app,
// including the closed World platform, can interoperate.
//
// Typical use:
//
//	w, _ := mvpack.NewWriter(dst, manifest)
//	_ = w.AddFile("refs/front.png", mvpack.RoleReference, pngReader)
//	_ = w.Close(&mvpack.Signer{KeyID: keyID, Key: priv, Role: mvpack.SignerServer})
//
//	p, _ := mvpack.Open(file, size, nil)
//	res, err := p.Verify(mvpack.VerifyOptions{Keys: trusted})
//
// Reading is streaming: payload files are never loaded into memory as a whole,
// every read is bounded by the declared and configured sizes, and hashes are
// checked when a stream reaches EOF.
package mvpack
