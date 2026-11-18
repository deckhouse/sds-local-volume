/*
Copyright 2026 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"strings"
	"testing"
)

func splitDMLVMName(name string) (vg, lv string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] != '-' {
			b.WriteByte(name[i])
			continue
		}
		if i+1 < len(name) && name[i+1] == '-' {
			b.WriteByte('-')
			i++
			continue
		}
		vg = b.String()
		lv = strings.ReplaceAll(name[i+1:], "--", "-")
		return vg, lv, true
	}
	return "", "", false
}

func isLVMName(s string) bool {
	return s != "" && s[0] != '-' && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+_.-") == ""
}

func FuzzToMapperPath(f *testing.F) {
	for _, seed := range [][2]string{
		{"some-good", "path"},
		{"vg-1", "lv-1"},
		{"vg--1", "pvc-0d2b6f4e-6b4f-4c4e-9c63-5b3b1e7c0a11"},
		{"a-", "-b"},
		{"vg", "lv--"},
		{"data", "thin-pool_tmeta"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, vg, lv string) {
		if got := toMapperPath(vg + "/" + lv); !strings.HasPrefix(vg+"/"+lv, "/dev/") && got != "" {
			t.Fatalf("toMapperPath(%q) = %q, want nothing for a path outside /dev", vg+"/"+lv, got)
		}
		if !isLVMName(vg) || !isLVMName(lv) {
			return
		}

		devPath := "/dev/" + vg + "/" + lv
		mapper := toMapperPath(devPath)
		name, ok := strings.CutPrefix(mapper, "/dev/mapper/")
		if !ok || strings.Contains(name, "/") {
			t.Fatalf("toMapperPath(%q) = %q, not a device-mapper node", devPath, mapper)
		}
		gotVG, gotLV, ok := splitDMLVMName(name)
		if !ok || gotVG != vg || gotLV != lv {
			t.Fatalf("toMapperPath(%q) = %q, which device-mapper reads as the VG %q and the LV %q", devPath, mapper, gotVG, gotLV)
		}
	})
}
