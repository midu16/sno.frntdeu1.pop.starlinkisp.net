package main

import (
    "bufio"
    "crypto/md5"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "strings"
    "time"
)

func main() {
    reader := bufio.NewReader(os.Stdin)
    fmt.Print("Enter full path to agent.x86_64.iso file: ")
    inputPath, err := reader.ReadString('\n')
    if err != nil {
        log.Fatalf("Failed to read input: %v", err)
    }
    inputPath = strings.TrimSpace(inputPath)

    etag, modTime, err := calculateETag(inputPath)
    if err != nil {
        log.Fatalf("Failed to calculate ETag for file %s: %v", inputPath, err)
    }

    http.HandleFunc("/agent.x86_64.iso", func(w http.ResponseWriter, r *http.Request) {
        if match := r.Header.Get("If-None-Match"); match != "" {
            if match == etag {
                w.WriteHeader(http.StatusNotModified)
                return
            }
        }

        file, err := os.Open(inputPath)
        if err != nil {
            http.Error(w, "File not found", http.StatusNotFound)
            return
        }
        defer file.Close()

        w.Header().Set("Content-Type", "application/octet-stream")
        w.Header().Set("Content-Disposition", "attachment; filename=agent.x86_64.iso")
        w.Header().Set("Cache-Control", "public, max-age=86400")
        w.Header().Set("ETag", etag)
        w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))

        http.ServeContent(w, r, "agent.x86_64.iso", modTime, file)
    })

    fmt.Println("Serving agent.x86_64.iso on http://0.0.0.0:9090/agent.x86_64.iso")
    log.Fatal(http.ListenAndServe(":9090", nil))
}

func calculateETag(path string) (string, time.Time, error) {
    f, err := os.Open(path)
    if err != nil {
        return "", time.Time{}, err
    }
    defer f.Close()

    h := md5.New()
    if _, err := io.Copy(h, f); err != nil {
        return "", time.Time{}, err
    }

    fi, err := f.Stat()
    if err != nil {
        return "", time.Time{}, err
    }
    etag := fmt.Sprintf(`"%x"`, h.Sum(nil))
    return etag, fi.ModTime(), nil
}
