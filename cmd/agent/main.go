package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type delta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
}

type choice struct {
	Delta        delta  `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type streamEvent struct {
	Choices []choice `json:"choices"`
}

func main() {
	url := flag.String("url", "http://localhost:3000/v1/chat/completions", "API endpoint")
	model := flag.String("model", "", "model name (optional)")
	system := flag.String("system", "", "system prompt")
	flag.Parse()

	var history []message
	if *system != "" {
		history = append(history, message{Role: "system", Content: *system})
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "/quit" || input == "/exit" {
			break
		}
		if input == "/clear" {
			history = history[:0]
			if *system != "" {
				history = append(history, message{Role: "system", Content: *system})
			}
			fmt.Println("(conversation cleared)")
			continue
		}

		history = append(history, message{Role: "user", Content: input})

		reply, err := chat(*url, *model, history)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			// Remove the failed user message
			history = history[:len(history)-1]
			continue
		}

		history = append(history, message{Role: "assistant", Content: reply})
		fmt.Println()
	}
}

func chat(url, model string, messages []message) (string, error) {
	body, _ := json.Marshal(request{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var full strings.Builder
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "data: ") {
			data := line[6:]
			if data == "[DONE]" {
				break
			}
			var event streamEvent
			if json.Unmarshal([]byte(data), &event) == nil && len(event.Choices) > 0 {
				d := event.Choices[0].Delta
				token := d.Content
				if token == "" {
					token = d.ReasoningContent
				}
				fmt.Print(token)
				full.WriteString(token)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return full.String(), err
		}
	}

	return full.String(), nil
}
