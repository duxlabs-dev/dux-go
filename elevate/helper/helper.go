// Package helper is the root side of a pkexec elevation.
package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/duxlabs-dev/dux-go/files"
)

// Ready is printed once a write is authorized and the file checks out; the caller waits for it.
const Ready = "ready"

const (
	ExitFailed  = 1
	ExitRefused = 2
)

type Failure struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// Run performs one operation; a non-nil result reaches the caller as the last stderr line.
type Run func(args []string, stdin io.Reader, stdout io.Writer) (any, error)

// UnderPkexec goes first in main, so a cached authorization reaches only the helper, never the rest of the app.
func UnderPkexec(app string, run Run) {
	if os.Getenv("PKEXEC_UID") == "" {
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "helper" {
		report(os.Stderr, Invalid(app+" runs only its helper under pkexec"))
		os.Exit(ExitRefused)
	}
	os.Exit(Main(app, os.Args[2:], os.Stdin, os.Stdout, os.Stderr, run))
}

func Main(app string, args []string, stdin io.Reader, stdout, stderr io.Writer, run Run) int {
	callerUID, err := strconv.Atoi(os.Getenv("PKEXEC_UID"))
	if err != nil {
		report(stderr, Invalid(app+" helper only runs under pkexec"))
		return ExitRefused
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		report(stderr, Invalid(app+" helper must be started as root"))
		return ExitRefused
	}
	if err := confine(); err != nil {
		report(stderr, err)
		return ExitRefused
	}
	unix.Umask(0)

	result, err := run(args, stdin, stdout)
	journal(app, callerUID, args, err)
	return Finish(stderr, result, err)
}

func Finish(stderr io.Writer, result any, err error) int {
	if err != nil {
		report(stderr, err)
		return ExitFailed
	}
	if result != nil {
		line, _ := json.Marshal(result)
		fmt.Fprintln(stderr, string(line))
	}
	return 0
}

func CodeOf(err error) files.Code {
	var coded *files.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return files.CodeFromOS(err)
}

func Invalid(msg string) error {
	return &files.Error{Code: files.CodeInvalid, Err: errors.New(msg)}
}

// The caller puts the code back in front, so the message goes without it.
func report(stderr io.Writer, err error) {
	code := string(CodeOf(err))
	line, _ := json.Marshal(Failure{Code: code, Error: strings.TrimPrefix(err.Error(), code+": ")})
	fmt.Fprintln(stderr, string(line))
}

func journal(app string, uid int, args []string, err error) {
	writer, openErr := syslog.New(syslog.LOG_AUTHPRIV|syslog.LOG_INFO, app+"-helper")
	if openErr != nil {
		return
	}
	defer writer.Close()
	outcome := "ok"
	if err != nil {
		outcome = "failed: " + err.Error()
	}
	_ = writer.Info(fmt.Sprintf("uid=%d %s %s", uid, strings.Join(args, " "), outcome))
}
