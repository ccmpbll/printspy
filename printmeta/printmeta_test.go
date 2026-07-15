package printmeta

import (
	"bytes"
	"os"
	"testing"
)

func TestParseBgcodeMK4SSpool(t *testing.T) {
	data, err := os.ReadFile("testdata/mk4s_spool.bgcode")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse("Spool Desiccant Holder 25_3_0.4n_0.25mm_PETG_COREONE_8h28m.bgcode", data)
	if err != nil {
		t.Fatal(err)
	}

	if info.PrinterModel != "MK4SMMU3" {
		t.Errorf("PrinterModel = %q, want MK4SMMU3", info.PrinterModel)
	}
	if info.Material != "PETG" {
		t.Errorf("Material = %q, want PETG", info.Material)
	}
	if info.ToolIndex != 1 {
		t.Errorf("ToolIndex = %d, want 1", info.ToolIndex)
	}
	if info.FillDensity != "20%" {
		t.Errorf("FillDensity = %q, want 20%%", info.FillDensity)
	}
	if info.LayerHeightMM != 0.25 {
		t.Errorf("LayerHeightMM = %v, want 0.25", info.LayerHeightMM)
	}
	if info.MaxLayerZ != 71.95 {
		t.Errorf("MaxLayerZ = %v, want 71.95", info.MaxLayerZ)
	}
	if got, want := info.FilamentUsedG, 124.02; abs(got-want) > 0.01 {
		t.Errorf("FilamentUsedG = %v, want %v", got, want)
	}
	if got, want := info.FilamentCost, 3.72; abs(got-want) > 0.001 {
		t.Errorf("FilamentCost = %v, want %v", got, want)
	}
	if info.EstimatedSecs != 28713 { // 7h 58m 33s
		t.Errorf("EstimatedSecs = %d, want 28713", info.EstimatedSecs)
	}
	if len(info.ObjectNames) != 6 {
		t.Errorf("len(ObjectNames) = %d, want 6", len(info.ObjectNames))
	}
	if len(info.Tools) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(info.Tools))
	}
	if info.ToolChanges != 0 {
		t.Errorf("ToolChanges = %d, want 0", info.ToolChanges)
	}
}

func TestParseGcodeBenchy(t *testing.T) {
	data, err := os.ReadFile("testdata/benchy.gcode")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse("benchy.gcode", data)
	if err != nil {
		t.Fatal(err)
	}

	if info.PrinterModel != "MK4SMMU3" {
		t.Errorf("PrinterModel = %q, want MK4SMMU3", info.PrinterModel)
	}
	if info.Material != "PLA" {
		t.Errorf("Material = %q, want PLA", info.Material)
	}
	if info.ToolIndex != 0 {
		t.Errorf("ToolIndex = %d, want 0", info.ToolIndex)
	}
	if info.LayerHeightMM != 0.2 {
		t.Errorf("LayerHeightMM = %v, want 0.2", info.LayerHeightMM)
	}
	if info.MaxLayerZ != 48 {
		t.Errorf("MaxLayerZ = %v, want 48", info.MaxLayerZ)
	}
	if got, want := info.FilamentUsedG, 13.54; abs(got-want) > 0.01 {
		t.Errorf("FilamentUsedG = %v, want %v", got, want)
	}
	if got, want := info.FilamentCost, 0.38; abs(got-want) > 0.001 {
		t.Errorf("FilamentCost = %v, want %v", got, want)
	}
	if info.EstimatedSecs != 3504 { // 58m 24s
		t.Errorf("EstimatedSecs = %d, want 3504", info.EstimatedSecs)
	}
	if len(info.ObjectNames) != 1 || info.ObjectNames[0] != "3dbenchy.stl" {
		t.Errorf("ObjectNames = %v, want [3dbenchy.stl]", info.ObjectNames)
	}
	if len(info.Tools) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(info.Tools))
	}
	if info.ToolChanges != 0 {
		t.Errorf("ToolChanges = %d, want 0", info.ToolChanges)
	}
}

func TestParseBgcodeMultiToolBenchy(t *testing.T) {
	data, err := os.ReadFile("testdata/multi_benchy.bgcode")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse("3dbenchy_0.4n_0.2mm_PLA,PLA_Ext0_2_MK4SMMU3_3h43m.bgcode", data)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Tools) != 2 {
		t.Fatalf("len(Tools) = %d, want 2", len(info.Tools))
	}
	if got := info.Tools[0]; got.ToolIndex != 0 || got.Material != "PLA" || abs(got.FilamentUsedG-26.28) > 0.01 || abs(got.FilamentCost-0.74) > 0.001 {
		t.Errorf("Tools[0] = %+v, want {0 PLA 26.28 0.74}", got)
	}
	if got := info.Tools[1]; got.ToolIndex != 2 || got.Material != "PLA" || abs(got.FilamentUsedG-14.78) > 0.01 || abs(got.FilamentCost-0.41) > 0.001 {
		t.Errorf("Tools[1] = %+v, want {2 PLA 14.78 0.41}", got)
	}
	if got, want := info.FilamentUsedG, 41.06; abs(got-want) > 0.01 {
		t.Errorf("FilamentUsedG = %v, want %v", got, want)
	}
	if got, want := info.FilamentCost, 1.15; abs(got-want) > 0.001 {
		t.Errorf("FilamentCost = %v, want %v", got, want)
	}
	if info.ToolChanges != 169 {
		t.Errorf("ToolChanges = %d, want 169", info.ToolChanges)
	}
	if info.Material != "PLA" {
		t.Errorf("Material = %q, want PLA (primary/first tool)", info.Material)
	}
	if info.ToolIndex != 0 {
		t.Errorf("ToolIndex = %d, want 0 (primary/first tool)", info.ToolIndex)
	}
}

func TestParseThumbnails(t *testing.T) {
	cases := []struct {
		file, name string
	}{
		{"testdata/mk4s_spool.bgcode", "spool.bgcode"},
		{"testdata/multi_benchy.bgcode", "benchy.bgcode"},
		{"testdata/benchy.gcode", "benchy.gcode"},
	}
	for _, c := range cases {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		info, err := Parse(c.name, data)
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Thumbnail) == 0 {
			t.Fatalf("%s: no thumbnail extracted", c.file)
		}
		if info.ThumbnailContentType != "image/png" {
			t.Errorf("%s: ThumbnailContentType = %q, want image/png", c.file, info.ThumbnailContentType)
		}
		if !bytes.HasPrefix(info.Thumbnail, []byte("\x89PNG\r\n\x1a\n")) {
			t.Errorf("%s: extracted thumbnail is not a valid PNG", c.file)
		}
	}
}

func TestSplitValuesAndDuration(t *testing.T) {
	if got := splitValues("PLA"); len(got) != 1 || got[0] != "PLA" {
		t.Errorf("scalar splitValues = %v", got)
	}
	if got := splitValues("PLA;PETG;PLA"); len(got) != 3 || got[1] != "PETG" {
		t.Errorf("semicolon splitValues = %v", got)
	}
	if got := splitValues("0.00, 40598.94, 0.00"); len(got) != 3 || got[1] != "40598.94" {
		t.Errorf("comma splitValues = %v", got)
	}
	if got := parseDuration("7h 58m 33s"); got != 28713 {
		t.Errorf("parseDuration = %d, want 28713", got)
	}
	if got := parseDuration("58m 24s"); got != 3504 {
		t.Errorf("parseDuration = %d, want 3504", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
