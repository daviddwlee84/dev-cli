// Package licenses resolves open-source license templates for repository
// scaffolding. It follows the same network -> cache -> bundled fallback shape
// as the gitignore package so repository creation remains useful offline.
package licenses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const API = "https://api.github.com/licenses"

type Source string

const (
	SourceRemote  Source = "github"
	SourceCache   Source = "cache"
	SourceBundled Source = "bundled"
)

type License struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Body   string `json:"body"`
	Source Source `json:"source"`
}

type Fetcher struct {
	CacheDir string
	Client   *http.Client
	Offline  bool
	NoWrite  bool
	BaseURL  string
}

func NewFetcher(cacheDir string) *Fetcher {
	return &Fetcher{CacheDir: cacheDir, Client: &http.Client{Timeout: 15 * time.Second}}
}

func (f *Fetcher) Get(ctx context.Context, key string) (License, error) {
	key = Canonical(key)
	if key == "" || key == "none" {
		return License{}, fmt.Errorf("license key is required")
	}
	if !f.Offline {
		if got, err := f.fetch(ctx, key); err == nil {
			got.Source = SourceRemote
			if !f.NoWrite {
				f.writeCache(got)
			}
			return got, nil
		}
	}
	if got, ok := f.readCache(key); ok {
		got.Source = SourceCache
		return got, nil
	}
	if body, ok := bundled[key]; ok {
		return License{Key: key, Name: bundledNames[key], Body: body, Source: SourceBundled}, nil
	}
	return License{}, fmt.Errorf("no license template for %q (offline, and no cached or bundled copy); available offline: %s",
		key, strings.Join(BundledKeys(), ", "))
}

func Canonical(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "apache", "apache2", "apache-2":
		return "apache-2.0"
	case "gpl", "gpl3", "gpl-3":
		return "gpl-3.0"
	case "bsd", "bsd3", "bsd-3":
		return "bsd-3-clause"
	case "mpl", "mpl2":
		return "mpl-2.0"
	}
	return key
}

func (f *Fetcher) fetch(ctx context.Context, key string) (License, error) {
	base := f.BaseURL
	if base == "" {
		base = API
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/"+key, nil)
	if err != nil {
		return License{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "dev-cli")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return License{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return License{}, fmt.Errorf("github returned %s for license %s", response.Status, key)
	}
	var payload struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Body string `json:"body"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&payload); err != nil {
		return License{}, err
	}
	if strings.TrimSpace(payload.Body) == "" {
		return License{}, fmt.Errorf("github returned an empty license template for %s", key)
	}
	if payload.Key == "" {
		payload.Key = key
	}
	return License{Key: payload.Key, Name: payload.Name, Body: strings.TrimRight(payload.Body, "\n")}, nil
}

func (f *Fetcher) cachePath(key string) string {
	if f.CacheDir == "" {
		return ""
	}
	return filepath.Join(f.CacheDir, key+".json")
}

func (f *Fetcher) writeCache(value License) {
	path := f.cachePath(value.Key)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	body, err := json.Marshal(struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Body string `json:"body"`
	}{value.Key, value.Name, value.Body})
	if err == nil {
		_ = os.WriteFile(path, append(body, '\n'), 0o644)
	}
}

func (f *Fetcher) readCache(key string) (License, bool) {
	body, err := os.ReadFile(f.cachePath(key))
	if err != nil {
		return License{}, false
	}
	var value License
	if json.Unmarshal(body, &value) != nil || value.Key == "" || value.Body == "" {
		return License{}, false
	}
	return value, true
}

// Render fills the placeholder spellings used by GitHub/ChooseALicense
// templates while leaving an unknown template untouched.
func Render(body, holder string, year int) string {
	if year == 0 {
		year = time.Now().Year()
	}
	if strings.TrimSpace(holder) == "" {
		holder = "Copyright Holder"
	}
	replacer := strings.NewReplacer(
		"[year]", fmt.Sprint(year), "[yyyy]", fmt.Sprint(year),
		"[fullname]", holder, "[name of copyright owner]", holder,
		"<year>", fmt.Sprint(year), "<copyright holders>", holder,
	)
	return strings.TrimRight(replacer.Replace(body), "\n") + "\n"
}

func BundledKeys() []string {
	keys := make([]string, 0, len(bundled))
	for key := range bundled {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var bundledNames = map[string]string{
	"bsd-3-clause": "BSD 3-Clause License",
	"isc":          "ISC License",
	"mit":          "MIT License",
	"unlicense":    "The Unlicense",
}

var bundled = map[string]string{
	"mit": `MIT License

Copyright (c) [year] [fullname]

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`,
	"isc": `ISC License

Copyright (c) [year] [fullname]

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM
LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR
OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THIS SOFTWARE.`,
	"bsd-3-clause": `BSD 3-Clause License

Copyright (c) [year], [fullname]
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.`,
	"unlicense": `This is free and unencumbered software released into the public domain.

Anyone is free to copy, modify, publish, use, compile, sell, or distribute this
software, either in source code form or as a compiled binary, for any purpose,
commercial or non-commercial, and by any means.

In jurisdictions that recognize copyright laws, the author or authors of this
software dedicate any and all copyright interest in the software to the public
domain. We make this dedication for the benefit of the public at large and to
the detriment of our heirs and successors. We intend this dedication to be an
overt act of relinquishment in perpetuity of all present and future rights to
this software under copyright law.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN
ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

For more information, please refer to <https://unlicense.org>`,
}
