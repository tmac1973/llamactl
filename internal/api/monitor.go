package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tmlabonte/llamactl/internal/monitor"
)

func (s *Server) handleMonitorStream(w http.ResponseWriter, r *http.Request) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ch := s.monitor.Subscribe()
	defer s.monitor.Unsubscribe(ch)

	// Send current state immediately
	data, _ := json.Marshal(s.monitor.Current())
	sse.SendEvent("metrics", string(data))

	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(m)
			sse.SendEvent("metrics", string(data))
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleMonitorStatus(w http.ResponseWriter, r *http.Request) {
	m := s.monitor.Current()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderMonitorBar(w, m)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func renderMonitorBar(w http.ResponseWriter, m monitor.Metrics) {
	fmt.Fprint(w, `<div style="display:flex;flex-direction:column;gap:0.4rem;">`)

	// GPU metrics
	for _, gpu := range m.GPU {
		vramPct := 0
		if gpu.VRAMTotalMB > 0 {
			vramPct = gpu.VRAMUsedMB * 100 / gpu.VRAMTotalMB
		}

		vramGB := fmt.Sprintf("%.1f/%.1fGB", float64(gpu.VRAMUsedMB)/1024, float64(gpu.VRAMTotalMB)/1024)

		fmt.Fprintf(w, `<div title="%s">`, gpu.Name)
		fmt.Fprintf(w, `<div style="display:flex;justify-content:space-between;"><span>GPU%d</span><span>%d%%</span></div>`, gpu.Index, gpu.UtilPercent)
		fmt.Fprintf(w, `<progress value="%d" max="100" style="width:100%%;height:6px;margin:0;"></progress>`, gpu.UtilPercent)
		fmt.Fprintf(w, `<div style="display:flex;justify-content:space-between;"><span>VRAM</span><span>%s</span></div>`, vramGB)
		fmt.Fprintf(w, `<progress value="%d" max="100" style="width:100%%;height:6px;margin:0;"></progress>`, vramPct)

		details := fmt.Sprintf("%d°C", gpu.TempC)
		if gpu.PowerW > 0 {
			details += fmt.Sprintf(" · %.0fW", gpu.PowerW)
		}
		fmt.Fprintf(w, `<div style="opacity:0.7;">%s</div>`, details)
		fmt.Fprint(w, `</div>`)
	}

	// CPU
	fmt.Fprintf(w, `<div><div style="display:flex;justify-content:space-between;"><span>CPU</span><span>%.0f%%</span></div>`, m.CPU.UsagePercent)
	fmt.Fprintf(w, `<progress value="%.0f" max="100" style="width:100%%;height:6px;margin:0;"></progress></div>`, m.CPU.UsagePercent)

	// RAM
	ramPct := 0
	if m.Memory.TotalMB > 0 {
		ramPct = m.Memory.UsedMB * 100 / m.Memory.TotalMB
	}
	ramGB := fmt.Sprintf("%.1f/%.1fGB", float64(m.Memory.UsedMB)/1024, float64(m.Memory.TotalMB)/1024)
	fmt.Fprintf(w, `<div><div style="display:flex;justify-content:space-between;"><span>RAM</span><span>%s</span></div>`, ramGB)
	fmt.Fprintf(w, `<progress value="%d" max="100" style="width:100%%;height:6px;margin:0;"></progress></div>`, ramPct)

	fmt.Fprint(w, `</div>`)
}
