package radio

import (
	"strings"
)

func SearchRadio(query string) []Station {
	term := strings.ToLower(strings.TrimSpace(query))
	found := make([]Station, 0, 10)

	for _, st := range AllStations {
		if term == "" {
			found = append(found, st)
			if len(found) == 10 {
				break
			}
			continue
		}
		if strings.Contains(strings.ToLower(st.Name), term) ||
			strings.Contains(strings.ToLower(st.Country), term) {
			found = append(found, st)
			if len(found) == 10 {
				break
			}
		}
	}
	return found
}
