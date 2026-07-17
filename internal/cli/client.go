package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func Post(apiURL, path string) error {
	client := &http.Client{Timeout: 20 * time.Second}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeSqueeze-Actor", "kubesqueeze-cli")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("API returned %s: %s", response.Status, body)
	}
	fmt.Print(string(body))
	return nil
}
