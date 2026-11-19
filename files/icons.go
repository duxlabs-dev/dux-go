package files

import (
	"strings"
	"sync"
)

const genericIcon = "text-x-generic"

type iconIndex struct {
	mu       sync.Mutex
	userDirs map[string]string
	stamp    userDirsStamp
	mimeIcon func(mimeType string) (specific, generic string)
}

func (i *iconIndex) dirs() map[string]string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.userDirs == nil {
		i.stamp = stampUserDirsFile()
		i.userDirs = loadUserDirs()
	}
	return i.userDirs
}

func (i *iconIndex) reloadChangedUserDirs() {
	stamp := stampUserDirsFile()
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.userDirs == nil || stamp == i.stamp {
		return
	}
	i.stamp = stamp
	i.userDirs = loadUserDirs()
}

func (i *iconIndex) best(e Entry) string {
	return i.ladder(e)[0]
}

func (i *iconIndex) ladder(e Entry) []string {
	switch {
	case e.Unreadable:
		return []string{"emblem-unreadable", genericIcon}
	case e.IsDir:
		return i.folderLadder(e.Path)
	case e.SymlinkBroken:
		return []string{"inode-symlink", "emblem-unreadable", genericIcon}
	}
	return i.decorate(mimeLadder(e.Mime, e.Executable), e.Mime)
}

// decorate ranks the mime database's icons above the naming-convention fallbacks.
func (i *iconIndex) decorate(ladder []string, mimeType string) []string {
	if i.mimeIcon == nil || mimeType == "" {
		return ladder
	}
	specific, generic := i.mimeIcon(mimeType)
	if specific != "" {
		ladder = append([]string{specific}, ladder...)
	}
	if generic == "" {
		return ladder
	}
	for cut, name := range ladder {
		if strings.HasSuffix(name, "-x-generic") {
			return append(append(append([]string{}, ladder[:cut]...), generic), ladder[cut:]...)
		}
	}
	return append(ladder, generic)
}

func (i *iconIndex) folderLadder(path string) []string {
	if icon, ok := i.dirs()[path]; ok {
		return []string{icon, "folder", "inode-directory"}
	}
	return []string{"folder", "inode-directory"}
}

func mimeLadder(mimeType string, executable bool) []string {
	if mimeType == "" {
		if executable {
			return []string{"application-x-executable", genericIcon}
		}
		return []string{genericIcon}
	}

	ladder := []string{strings.ReplaceAll(mimeType, "/", "-")}
	if executable {
		ladder = append(ladder, "application-x-executable")
	}
	if media := mediaType(mimeType); media != "" {
		ladder = append(ladder, media+"-x-generic")
	}
	if ladder[len(ladder)-1] != genericIcon {
		ladder = append(ladder, genericIcon)
	}
	return ladder
}
