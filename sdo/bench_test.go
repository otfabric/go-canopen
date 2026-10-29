// SPDX-License-Identifier: MIT

package sdo

import (
	"testing"
)

func BenchmarkExpeditedUpload(b *testing.B) {
	c, _, _ := setup(b, nil)
	ctx := ctxT(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Upload(ctx, 0x1000, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlockDownload4K(b *testing.B) {
	c, _, _ := setup(b, nil, WithBlockMode(BlockAlways))
	ctx := ctxT(b)
	data := pattern(4096)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Download(ctx, idxDomain, 0, data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlockUpload4K(b *testing.B) {
	c, _, d := setup(b, nil, WithBlockMode(BlockAlways))
	ctx := ctxT(b)
	_ = d.Set(idxDomain, 0, pattern(4096))
	b.SetBytes(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Upload(ctx, idxDomain, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSegmentedUpload4K(b *testing.B) {
	c, _, d := setup(b, nil)
	ctx := ctxT(b)
	_ = d.Set(idxDomain, 0, pattern(4096))
	b.SetBytes(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Upload(ctx, idxDomain, 0); err != nil {
			b.Fatal(err)
		}
	}
}
