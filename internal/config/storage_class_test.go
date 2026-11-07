package config

import "testing"

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

var _ = Describe("storage classes", func() {
	It("maps class names to VGs and includes them in the startup seed", func() {
		cfg, err := Parse([]byte(strings.Replace(validConfig, "  vgs: [vg1]", `  vgs: [vg1]
  classes:
    nvme: vg-nvme
    ssd: vg1
    hdd: vg-hdd`, 1)))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.Storage.Classes).To(gomega.Equal(map[string]string{
			"nvme": "vg-nvme",
			"ssd":  "vg1",
			"hdd":  "vg-hdd",
		}))
		gomega.Expect(cfg.EffectiveVGs()).To(gomega.Equal([]string{"vg1", "vg-hdd", "vg-nvme"}))
	})

	It("rejects two classes for the same VG", func() {
		_, err := Parse([]byte(strings.Replace(validConfig, "  vgs: [vg1]", `  vgs: []
  classes:
    nvme: vg1
    ssd: vg1`, 1)))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("reference the same VG")))
	})

	It("rejects invalid class names", func() {
		_, err := Parse([]byte(strings.Replace(validConfig, "  vgs: [vg1]", `  vgs: []
  classes:
    "bad class": vg1`, 1)))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("invalid storage.classes name")))
	})
})

func TestStorageClassForVGFallback(t *testing.T) {
	if got := StorageClassForVG(nil, "vg1"); got != "vg1" {
		t.Fatalf("nil fallback = %q", got)
	}
	if got := StorageClassForVG(map[string]string{"ssd": "vg1"}, "vg1"); got != "ssd" {
		t.Fatalf("mapped = %q", got)
	}
	if got := StorageClassForVG(map[string]string{"ssd": "vg1"}, "vg2"); got != "vg2" {
		t.Fatalf("unmapped = %q", got)
	}
}
