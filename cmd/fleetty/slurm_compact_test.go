package main

import (
	"fmt"
	"image/color"
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
				plain := renderSlurmJobRow(job, width, true, lipgloss.NewStyle())
				selected := renderSlurmJobRow(job, width, true, base)
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
			header, row := slurmJobTableHeader(width, true), renderSlurmJobRow(job, width, true, lipgloss.NewStyle())
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
			if width >= 54 && (!strings.Contains(r, "144762 ") || !strings.Contains(r, "ext-hostfast")) {
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
		job := slurmDisplayJob{Job: slurmJob{
			ID: "144886_[8-15%2]", State: state, Elapsed: "0:00", QOS: "custom-qos",
			Name: "训练任务-with-a-long-name", Reason: "Resources",
		}}
		for _, width := range []int{32, 40, 56, 80, 111} {
			row := renderSlurmJobRow(job, width, true, lipgloss.NewStyle())
			if lipgloss.Width(row) > width || !strings.Contains(row, "0:00") {
				t.Fatalf("invalid compact row: %s", row)
			}
			if width >= 56 && !strings.Contains(row, "训练任务") {
				t.Fatalf("Unicode job name lost: %s", row)
			}
		}
	}

}

func TestSlurmQOSColorsAndLegend(t *testing.T) {
	if slurmQOSStyle("urgent").Render("●") != dangerStyle.Render("●") {
		t.Fatal("urgent QOS should use the critical state color")
	}
	for _, qos := range []string{"high", "low", "normal", "long", "custom"} {
		if slurmQOSStyle(qos).Render("●") == slurmQOSStyle("").Render("●") {
			t.Fatalf("QOS %s looks like a missing value", qos)
		}
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
	if slurmQOSStyle("(null)").Render("●") != slurmQOSStyle("").Render("●") || lipgloss.Width(slurmQOSLegend(jobs, 12)) > 12 {
		t.Fatal("invalid missing-QOS or narrow legend handling")
	}
}

func TestSlurmQOSColorsAreReadableAndDistinct(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		for _, indexed := range []bool{false, true} {
			canvas, _ := viewColors(mode)
			selected := selectedProcessStyle(mode).GetBackground()
			if indexed {
				canvas = ansi.Convert256(canvas)
			}
			seen := map[color.Color]bool{}
			for _, name := range []string{"high", "low", "normal"} {
				fg := themeForeground(t, slurmQOSStyle(name), mode)
				if indexed {
					fg = ansi.Convert256(fg)
				}
				if seen[fg] || themeContrast(fg, canvas) < 4.5 || themeContrast(fg, selected) < 4.5 {
					t.Errorf("%s/%t: QOS %s is not distinct and readable (canvas %.2f, selected %.2f)", mode, indexed, name, themeContrast(fg, canvas), themeContrast(fg, selected))
				}
				seen[fg] = true
			}
		}
	}
}

func TestSlurmArraySuffixHiddenInListsButNotDetails(t *testing.T) {
	for _, id := range []string{"315058", "315039_44", "312889_[2-5%1]"} {
		base := strings.SplitN(id, "_", 2)[0]
		job := slurmDisplayJob{Cluster: "Cluster", Job: slurmJob{ID: id, User: "alice", State: "PENDING", QOS: "low"}}
		for _, width := range []int{36, 96, 112, 152, 200} {
			for _, row := range []string{renderSlurmJobRow(job, width, true, lipgloss.NewStyle()), renderSlurmNodeJobRow(job, width)} {
				plain := ansi.Strip(row)
				if !strings.Contains(plain, base+" ") || strings.Contains(plain, "_") {
					t.Fatalf("width %d: suffix leaked or base ID is missing: %s", width, plain)
				}
			}
		}
		m := &hubModel{width: 120, height: 24, queueExpanded: true, slurmExplain: true, slurmFilter: -1,
			config:      hubConfig{SlurmClusters: []slurmClusterConfig{{Name: "Cluster"}}},
			slurmStates: []slurmClusterState{{Snapshot: slurmSnapshot{Name: "Cluster", CollectedAt: time.Now(), Jobs: []slurmJob{job.Job}}}},
		}
		if !strings.Contains(ansi.Strip(m.hubView()), id) {
			t.Fatalf("job details lost full ID %s", id)
		}
	}
}

func TestCompactSlurmLegendDoesNotStealJobSelection(t *testing.T) {
	jobs := make([]slurmJob, 40)
	for i := range jobs {
		jobs[i] = slurmJob{ID: fmt.Sprintf("JOB%03d", i), User: "yuechen", State: "RUNNING", QOS: "normal", Elapsed: "1:23", Name: "train"}
	}
	for _, width := range []int{36, 60, 96, 116, 160} {
		m := &hubModel{width: width, height: 24, queueExpanded: true, slurmFilter: -1, slurmCursor: 39,
			config:      hubConfig{Name: "Hub", SlurmClusters: []slurmClusterConfig{{Name: "Cluster"}}},
			slurmStates: []slurmClusterState{{Snapshot: slurmSnapshot{Name: "Cluster", CollectedAt: time.Now(), Jobs: jobs}}},
		}
		view := m.hubView()
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
		for _, header := range []string{slurmJobTableHeader(width, true), slurmNodeJobHeader(width)} {
			if !strings.Contains(header, "WEIGHT") || !strings.Contains(header, "QOS") {
				t.Fatalf("wide header lost columns: %s", header)
			}
		}
		for _, row := range []string{renderSlurmJobRow(job, width, true, lipgloss.NewStyle()), renderSlurmNodeJobRow(job, width)} {
			if !strings.Contains(row, "3094") || !strings.Contains(row, "urgent") {
				t.Fatalf("wide row lost fields: %s", row)
			}
		}
	}
}

func TestSlurmTimeHeadersMatchRowAlignment(t *testing.T) {
	rightEdge := func(line, value string) int {
		t.Helper()
		line = ansi.Strip(line)
		index := strings.Index(line, value)
		if index < 0 {
			t.Fatalf("missing %q in %q", value, line)
		}
		return lipgloss.Width(line[:index]) + lipgloss.Width(value)
	}
	for _, width := range []int{56, 96, 111, 112, 151, 152, 200} {
		for _, node := range []bool{false, true} {
			for _, elapsed := range []string{"0:00", "3:30", "23:59:59", "12-23:59:59"} {
				t.Run(fmt.Sprintf("width=%d/node=%v/elapsed=%s", width, node, elapsed), func(t *testing.T) {
					job := slurmDisplayJob{Cluster: "SSSLab", Job: slurmJob{
						ID: "144566", User: "lhc", State: "RUNNING", QOS: "normal",
						Elapsed: elapsed, TimeLimit: "20:00", Name: "training",
					}}
					header, row := slurmJobTableHeader(width, true), renderSlurmJobRow(job, width, true, lipgloss.NewStyle())
					if !node && width >= slurmCompactWidth {
						timeWidth := 17
						if width >= 152 {
							timeWidth = 19
						}
						if rightEdge(header, "TIME") != rightEdge(row, slurmQueueTime(job.Job, timeWidth)) {
							t.Fatalf("TIME is not right-aligned\nheader: %q\nrow:    %q", ansi.Strip(header), ansi.Strip(row))
						}
						return
					}
					elapsedWidth := 11
					if width >= slurmCompactWidth {
						elapsedWidth = 8
						if node || width >= 152 {
							elapsedWidth = 9
						}
					}
					if node {
						header, row = slurmNodeJobHeader(width), renderSlurmNodeJobRow(job, width)
					}
					visibleElapsed := strings.TrimSpace(fixedCell(elapsed, elapsedWidth, true))
					if rightEdge(header, "ELAPSED") != rightEdge(row, visibleElapsed) {
						t.Fatalf("ELAPSED is not right-aligned\nheader: %q\nrow:    %q", ansi.Strip(header), ansi.Strip(row))
					}
					if strings.Contains(ansi.Strip(header), "LIMIT") && rightEdge(header, "LIMIT") != rightEdge(row, job.Job.TimeLimit) {
						t.Fatalf("LIMIT is not right-aligned\nheader: %q\nrow:    %q", ansi.Strip(header), ansi.Strip(row))
					}
				})
			}
		}
	}
}

func TestSlurmQueueTime(t *testing.T) {
	for _, tc := range []struct {
		elapsed, limit string
		width          int
		want           string
	}{
		{"4:20", "30:00", 17, "4:20 / 30:00"},
		{"0:00", "UNLIMITED", 17, "0:00 / UNLIMITED"},
		{"2:30", "12-00:00:00", 17, "2:30/12-00:00:00"},
		{"23:59:59", "UNLIMITED", 19, "23:59:59/UNLIMITED"},
		{"12-23:59:59", "UNLIMITED", 17, "12-23:…/UNLIMITED"},
		{"12-23:59:59", "365-00:00:00", 0, "12-23:59:59 / 365-00:00:00"},
		{"4:20", "N/A", 17, "4:20 / —"},
		{"", "30:00", 17, "— / 30:00"},
	} {
		job := slurmJob{Elapsed: tc.elapsed, TimeLimit: tc.limit}
		if got := slurmQueueTime(job, tc.width); got != tc.want {
			t.Errorf("elapsed %q, limit %q, width %d: got %q, want %q", tc.elapsed, tc.limit, tc.width, got, tc.want)
		}
	}
}

func TestCompactSlurmStateCodes(t *testing.T) {
	for _, test := range []struct {
		state string
		want  string
	}{
		{"RUNNING", "R"},
		{"COMPLETING", "C"},
		{"PENDING", "P"},
		{"SUSPENDED", "S"},
	} {
		job := slurmDisplayJob{Job: slurmJob{
			ID: "145020", User: "yuechen", State: test.state, Elapsed: "1:23", Name: "training",
		}}
		for _, row := range []string{renderSlurmJobRow(job, 56, true, lipgloss.NewStyle()), renderSlurmNodeJobRow(job, 56)} {
			fields := strings.Fields(ansi.Strip(row))
			if len(fields) != 6 || fields[2] != "yuechen" || fields[3] != test.want || fields[4] != "1:23" || fields[5] != "training" {
				t.Fatalf("state %s: %v", test.state, fields)
			}
		}
	}
}
