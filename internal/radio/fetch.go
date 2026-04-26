package radio

import (
	"discordAudio/internal/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func LoadAllStations() error {
	stations, err := getAvailableRadios()
	if err != nil {
		return err
	}

	// Очищаем прошлые значения
	AllStations = make([]Station, 0, len(stations))

	for _, st := range stations {
		if st.StreamURL != "" {
			AllStations = append(AllStations, st)
		}
	}
	return nil
}

type Mirror struct {
	URL string `json:"name"`
}

func getAvailableRadios() ([]Station, error) {
	mirror, err := getAvailableMirror()
	if err != nil {
		return nil, fmt.Errorf("failed to get stations: %w", err)
	}
	resp, err := http.Get(fmt.Sprintf("https://%s/json/stations", mirror))
	//resp, err := http.Get(mirror)

	if err != nil {
		return nil, fmt.Errorf("failed to get stations: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Send(fmt.Sprintf("failed to close body getAvailableRadios: %v", err))
		}
	}(resp.Body)

	var stations []Station

	if err := json.NewDecoder(resp.Body).Decode(&stations); err != nil {
		return nil, fmt.Errorf("failed to decode stations JSON: %w", err)
	}
	return stations, nil
}

func getAvailableMirror() (string, error) {
	resp, err := http.Get("https://all.api.radio-browser.info/json/servers")
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Send(fmt.Sprintf("failed to close body getAvailableMirror: %v", err))
		}
	}(resp.Body)
	var mirrors []Mirror

	if err := json.NewDecoder(resp.Body).Decode(&mirrors); err != nil {
		return "", err
	}

	if len(mirrors) == 0 {
		return "", fmt.Errorf("no mirrors found")
	}

	return mirrors[0].URL, nil
}
