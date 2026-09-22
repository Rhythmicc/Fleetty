package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestSlurmSelectionPreservesColorsAcrossEntireRow(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		base := selectedProcessStyle(mode)
		wantBG := uv.NewStyledString(base.Render(" ")).Lines(ansi.GraphemeWidth)[0][0].Style.Bg
		for _, width := range []int{32, 56, 96, 111, 112, 152, 200} {
			for _, qos := range []string{"normal", "high", "urgent", "long", "low", "custom"} {
				job := slurmDisplayJob{Cluster: "SSSLab", Job: slurmJob{
					ID: "145020", User: "yuechen", State: "RUNNING", QOS: qos,
					Elapsed: "1:23", Name: "train", NodeList: "node01",
				}}
				plain := renderSlurmJobRow(job, width)
				selected := renderSlurmJobRowWithStyle(job, width, base)
				if ansi.Strip(plain) != ansi.Strip(selected) {
					t.Fatalf("selection changed content at width %d", width)
				}
				before := uv.NewStyledString(plain).Lines(ansi.GraphemeWidth)[0]
				after := uv.NewStyledString(selected).Lines(ansi.GraphemeWidth)[0]
				for x, cell := range after {
					if !reflect.DeepEqual(cell.Style.Bg, wantBG) {
						t.Fatalf("width %d QOS %s cell %d (%q) lost selection background", width, qos, x, cell.Content)
					}
					if before[x].Style.Fg != nil && !reflect.DeepEqual(cell.Style.Fg, before[x].Style.Fg) {
						t.Fatalf("width %d QOS %s cell %d (%q) changed semantic color", width, qos, x, cell.Content)
					}
				}
			}
		}
	}
}

func TestCompactSlurmRowsPrioritizeElapsedAndName(t *testing.T) {
	job := slurmDisplayJob{Cluster: "SSSLab", Job: slurmJob{
		ID: "144762_19", User: "lhc", State: "RUNNING", Priority: 87654321,
		QOS: "urgent", Elapsed: "12-23:59:59", Name: "ext-hostfast-full",
	}}
	for width := 32; width < slurmCompactWidth; width++ {
		for _, node := range []bool{false, true} {
			header, row := slurmJobTableHeader(width), renderSlurmJobRow(job, width)
			if node {
				header, row = slurmNodeJobHeader(width), renderSlurmNodeJobRow(job, width)
			}
			h, r := ansi.Strip(header), ansi.Strip(row)
			if lipgloss.Width(header) > width || lipgloss.Width(row) > width {
				t.Fatalf("width %d node=%v overflows: %q", width, node, r)
			}
			if strings.Contains(h, "WEIGHT") || strings.Contains(h, "QOS") || strings.Contains(r, "87654321") || strings.Contains(r, "urgent") {
				t.Fatalf("width %d retained weight/QOS columns: %s / %s", width, h, r)
			}
			if !strings.Contains(r, "12-23:59:59") || !strings.Contains(r, " R ") || !strings.Contains(r, "● ") || !strings.Contains(r, "lhc") || !strings.Contains(h, "USER") {
				t.Fatalf("width %d lost required compact data: %s", width, r)
			}
			if width >= 54 && (!strings.Contains(r, "144762_19") || !strings.Contains(r, "ext-hostfast")) {
				t.Fatalf("phone width %d lost job ID/name: %s", width, r)
			}
			if lipgloss.Width(h[:strings.Index(h, "ELAPSED")])+len("ELAPSED") != lipgloss.Width(r[:strings.Index(r, job.Job.Elapsed)])+len(job.Job.Elapsed) {
				t.Fatalf("elapsed header/data not right-aligned at width %d", width)
			}
		}
	}
}

func TestCompactSlurmStatesArraysAndUnicode(t *testing.T) {
	for _, state := range []string{"RUNNING", "PENDING", "COMPLETING", "SUSPENDED"} {
		for _, next := range []bool{false, true} {
			job := slurmDisplayJob{Next: next, Job: slurmJob{
				ID: "144886_[8-15%2]", State: state, Elapsed: "0:00", QOS: "custom-qos",
				Name: "训练任务-with-a-long-name", Reason: "Resources",
			}}
			for _, width := range []int{32, 40, 56, 80, 111} {
				row := renderSlurmJobRow(job, width)
				if lipgloss.Width(row) > width || !strings.Contains(row, "0:00") {
					t.Fatalf("invalid compact row: %s", row)
				}
				if width >= 56 && !strings.Contains(row, "训练任务") {
					t.Fatalf("Unicode job name lost: %s", row)
				}
			}
		}
	}

}

func TestSlurmQOSColorsAndLegend(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"normal", "high", "urgent", "long", "low"} {
		color := fmt.Sprint(slurmQOSStyle(name).GetForeground())
		if seen[color] {
			t.Fatalf("standard QOS %s reused color %s", name, color)
		}
		seen[color] = true
	}
	jobs := []slurmDisplayJob{{Job: slurmJob{QOS: "urgent"}}, {Job: slurmJob{QOS: "normal"}}, {Job: slurmJob{QOS: "urgent"}}}
	legend := slurmQOSLegend(jobs, 56)
	if got := ansi.Strip(legend); got != "QOS  ● normal  ● urgent" {
		t.Fatalf("legend = %q", got)
	}
	jobs[0], jobs[1] = jobs[1], jobs[0]
	if legend != slurmQOSLegend(jobs, 56) {
		t.Fatal("legend changes when queue order changes")
	}
	if slurmQOSDot("(null)") != slurmQOSDot("") || lipgloss.Width(slurmQOSLegend(jobs, 12)) > 12 {
		t.Fatal("invalid missing-QOS or narrow legend handling")
	}
}

func TestCompactSlurmLegendDoesNotStealJobSelection(t *testing.T) {
	jobs := make([]slurmJob, 40)
	for i := range jobs {
		jobs[i] = slurmJob{ID: fmt.Sprintf("JOB%03d", i), User: "yuechen", State: "RUNNING", QOS: "normal", Elapsed: "1:23", Name: "train"}
	}
	for _, width := range []int{36, 60, 96, 116, 160} {
		m := &hubModel{width: width, height: 24, slurmFilter: -1, slurmCursor: 39,
			config:      hubConfig{Name: "Hub", SlurmClusters: []slurmClusterConfig{{Name: "Cluster"}}},
			slurmStates: []slurmClusterState{{Snapshot: slurmSnapshot{Name: "Cluster", CollectedAt: time.Now(), Jobs: jobs}}},
		}
		view := m.slurmQueueView()
		if lipgloss.Height(view) != 24 || !strings.Contains(view, "JOB039") {
			t.Fatalf("width %d lost viewport/selected job", width)
		}
		for y, line := range strings.Split(ansi.Strip(view), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d overflows", width)
			}
			if strings.Contains(line, "JOB039") {
				if index, ok := m.slurmJobAt(3, y); !ok || index != 39 {
					t.Fatalf("selected row click = %d, %v", index, ok)
				}
			}
			if strings.Contains(line, "QOS  ●") {
				if _, ok := m.slurmJobAt(3, y); ok {
					t.Fatal("QOS legend treated as a job")
				}
			}
		}
		if width == 60 {
			if !strings.Contains(view, "yuechen") || !strings.Contains(view, "USER") {
				t.Fatal("phone view lost the username column")
			}
			t.Logf("phone preview:\n%s", ansi.Strip(view))
		}
	}
}

func TestWideSlurmKeepsWeightAndQOS(t *testing.T) {
	job := slurmDisplayJob{Job: slurmJob{ID: "144566", QOS: "urgent", Priority: 3094, Elapsed: "1:23"}}
	for _, width := range []int{112, 152, 200} {
		for _, header := range []string{slurmJobTableHeader(width), slurmNodeJobHeader(width)} {
			if !strings.Contains(header, "WEIGHT") || !strings.Contains(header, "QOS") {
				t.Fatalf("wide header lost columns: %s", header)
			}
		}
		for _, row := range []string{renderSlurmJobRow(job, width), renderSlurmNodeJobRow(job, width)} {
			if !strings.Contains(row, "3094") || !strings.Contains(row, "urgent") {
				t.Fatalf("wide row lost fields: %s", row)
			}
		}
	}
}

func TestCompactSlurmStateCodes(t *testing.T) {
	for _, test := range []struct {
		state string
		next  bool
		want  string
	}{
		{"RUNNING", false, "R"},
		{"COMPLETING", false, "R"},
		{"PENDING", true, "N"},
		{"PENDING", false, "Q"},
		{"SUSPENDED", false, "S"},
	} {
		job := slurmDisplayJob{Next: test.next, Job: slurmJob{
			ID: "145020", User: "yuechen", State: test.state, Elapsed: "1:23", Name: "training",
		}}
		for _, render := range []func(slurmDisplayJob, int) string{renderSlurmJobRow, renderSlurmNodeJobRow} {
			fields := strings.Fields(ansi.Strip(render(job, 56)))
			if len(fields) != 6 || fields[2] != "yuechen" || fields[3] != test.want || fields[4] != "1:23" || fields[5] != "training" {
				t.Fatalf("state %s next=%v: %v", test.state, test.next, fields)
			}
		}
	}
}
