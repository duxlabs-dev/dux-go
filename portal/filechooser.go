package portal

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/godbus/dbus/v5"
)

const fileChooserInterface = "org.freedesktop.portal.FileChooser"

// ErrNoPortal indicates no portal implements the call.
var ErrNoPortal = errors.New("portal: not available")

type OpenFileOptions struct {
	Title    string
	Dir      string
	Multiple bool
}

type SaveFileOptions struct {
	Title string
	Dir   string
	Name  string
}

// OpenFile returns the local paths picked in the portal's open dialog.
func OpenFile(ctx context.Context, conn *dbus.Conn, opts OpenFileOptions) ([]string, error) {
	options := map[string]dbus.Variant{"multiple": dbus.MakeVariant(opts.Multiple)}
	if opts.Dir != "" {
		options["current_folder"] = folderVariant(opts.Dir)
	}
	uris, err := chooseFile(ctx, conn, "OpenFile", opts.Title, options)
	if err != nil {
		return nil, err
	}
	return localPaths(uris), nil
}

// SaveFile returns the local path picked in the portal's save dialog.
func SaveFile(ctx context.Context, conn *dbus.Conn, opts SaveFileOptions) (string, error) {
	options := map[string]dbus.Variant{}
	if opts.Dir != "" {
		options["current_folder"] = folderVariant(opts.Dir)
	}
	if opts.Name != "" {
		options["current_name"] = dbus.MakeVariant(opts.Name)
	}
	uris, err := chooseFile(ctx, conn, "SaveFile", opts.Title, options)
	if err != nil {
		return "", err
	}
	paths := localPaths(uris)
	if len(paths) == 0 {
		return "", errors.New("portal: the file chooser returned no local file")
	}
	return paths[0], nil
}

// The portal wants a NUL-terminated byte string, not a D-Bus string.
func folderVariant(dir string) dbus.Variant {
	return dbus.MakeVariant(append([]byte(dir), 0))
}

func chooseFile(ctx context.Context, conn *dbus.Conn, method, title string, options map[string]dbus.Variant) ([]string, error) {
	token := newHandleToken()
	options["handle_token"] = dbus.MakeVariant(token)
	waiter, err := newResponseWaiter(conn, requestPath(conn, token))
	if err != nil {
		return nil, err
	}
	defer waiter.close()

	var handle dbus.ObjectPath
	call := conn.Object(busName, objectPath).CallWithContext(ctx, fileChooserInterface+"."+method, 0, "", title, options)
	if err := call.Store(&handle); err != nil {
		// A canceled call may still have opened the dialog.
		if ctx.Err() != nil {
			waiter.closeRequest()
		}
		return nil, callFailure(ctx, err)
	}
	if err := waiter.redirect(handle); err != nil {
		return nil, err
	}

	results, err := waiter.wait(ctx)
	if err != nil {
		return nil, err
	}
	uris, _ := results["uris"].Value().([]string)
	return uris, nil
}

func callFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch errorName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.UnknownObject":
		return ErrNoPortal
	}
	return fmt.Errorf("portal file chooser: %w", err)
}

// godbus returns both dbus.Error and *dbus.Error.
func errorName(err error) string {
	var value dbus.Error
	if errors.As(err, &value) {
		return value.Name
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) {
		return ptr.Name
	}
	return ""
}

func localPaths(uris []string) []string {
	paths := make([]string, 0, len(uris))
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			continue
		}
		if u.Host != "" && u.Host != "localhost" {
			continue
		}
		paths = append(paths, u.Path)
	}
	return paths
}
