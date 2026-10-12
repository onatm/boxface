package images

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/id"
)

var (
	ErrInvalidSource = errors.New("invalid import source")
	ErrInvalidAlias  = errors.New("invalid image alias")
	ErrNotFound      = errors.New("image not found")
	ErrInUse         = errors.New("image is in use")
	sourcePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	aliasPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	artifacts        = []string{"vmlinux", "rootfs.ext4"}
)

type Image struct {
	ID        string   `json:"id"`
	Digest    string   `json:"digest"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	Aliases   []string `json:"aliases"`
}

type Registry struct {
	db        *sql.DB
	importDir string
	imagesDir string
}

func NewRegistry(db *sql.DB, importDir, imagesDir string) *Registry {
	return &Registry{db: db, importDir: importDir, imagesDir: imagesDir}
}

func (r *Registry) Import(ctx context.Context, source string) (Image, bool, error) {
	if !sourcePattern.MatchString(source) {
		return Image{}, false, fmt.Errorf("%w: %q must match %s", ErrInvalidSource, source, sourcePattern)
	}
	src := filepath.Join(r.importDir, source)
	digest := sha256.New()
	for _, name := range artifacts {
		info, err := os.Lstat(filepath.Join(src, name))
		if err != nil || !info.Mode().IsRegular() {
			return Image{}, false, fmt.Errorf("%w: %s/%s must be a regular file", ErrInvalidSource, source, name)
		}
		f, err := os.Open(filepath.Join(src, name))
		if err != nil {
			return Image{}, false, err
		}
		fmt.Fprintf(digest, "%s\n", name)
		_, err = io.Copy(digest, f)
		f.Close()
		if err != nil {
			return Image{}, false, err
		}
	}
	sum := "sha256:" + hex.EncodeToString(digest.Sum(nil))

	var removed sql.NullString
	var existingID string
	err := r.db.QueryRowContext(ctx, `SELECT id, removed_at FROM images WHERE digest = ?`, sum).Scan(&existingID, &removed)
	if err == nil && !removed.Valid {
		existing, err := r.byDigest(ctx, sum)
		return existing, false, err
	}
	if err == nil {
		dest := filepath.Join(r.imagesDir, existingID)
		if err := os.Rename(src, dest); err != nil {
			return Image{}, false, err
		}
		os.Chmod(dest, 0o755)
		if _, err := r.db.ExecContext(ctx, `UPDATE images SET removed_at = NULL WHERE id = ?`, existingID); err != nil {
			return Image{}, false, err
		}
		restored, err := r.byDigest(ctx, sum)
		return restored, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Image{}, false, err
	}

	img := Image{ID: id.New(), Digest: sum, Status: "ready", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Aliases: []string{}}
	dest := filepath.Join(r.imagesDir, img.ID)
	if err := os.Rename(src, dest); err != nil {
		return Image{}, false, err
	}
	os.Chmod(dest, 0o755)
	for _, name := range artifacts {
		os.Chmod(filepath.Join(dest, name), 0o644)
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO images (id, digest, status, created_at) VALUES (?, ?, ?, ?)`, img.ID, img.Digest, img.Status, img.CreatedAt); err != nil {
		os.Rename(dest, src)
		return Image{}, false, err
	}
	return img, true, nil
}

type Staged struct {
	Source  string `json:"source"`
	SizeMiB int64  `json:"size_mib"`
}

func (r *Registry) Staged() ([]Staged, error) {
	entries, err := os.ReadDir(r.importDir)
	if err != nil {
		return nil, err
	}
	list := []Staged{}
	for _, e := range entries {
		if !e.IsDir() || !sourcePattern.MatchString(e.Name()) {
			continue
		}
		staged := Staged{Source: e.Name()}
		complete := true
		for _, name := range artifacts {
			info, err := os.Lstat(filepath.Join(r.importDir, e.Name(), name))
			if err != nil || !info.Mode().IsRegular() {
				complete = false
				break
			}
			staged.SizeMiB += info.Size() >> 20
		}
		if complete {
			list = append(list, staged)
		}
	}
	return list, nil
}

func (r *Registry) Remove(ctx context.Context, imageID string) error {
	var removed sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT removed_at FROM images WHERE id = ?`, imageID).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) || removed.Valid {
		return fmt.Errorf("%w: %s", ErrNotFound, imageID)
	}
	if err != nil {
		return err
	}
	aliases, err := r.aliases(ctx, imageID)
	if err != nil {
		return err
	}
	if len(aliases) > 0 {
		return fmt.Errorf("%w: alias %s points to it; move the alias first", ErrInUse, strings.Join(aliases, ", "))
	}
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM environments WHERE image_id = ? AND observed_state <> 'deleted' ORDER BY name`, imageID)
	if err != nil {
		return err
	}
	var users []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		users = append(users, name)
	}
	rows.Close()
	if len(users) > 0 {
		return fmt.Errorf("%w: environments %s use it", ErrInUse, strings.Join(users, ", "))
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE images SET removed_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), imageID); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(r.imagesDir, imageID))
}

func (r *Registry) byDigest(ctx context.Context, digest string) (Image, error) {
	img := Image{Digest: digest}
	err := r.db.QueryRowContext(ctx, `SELECT id, status, created_at FROM images WHERE digest = ?`, digest).Scan(&img.ID, &img.Status, &img.CreatedAt)
	if err != nil {
		return img, err
	}
	img.Aliases, err = r.aliases(ctx, img.ID)
	return img, err
}

func (r *Registry) aliases(ctx context.Context, imageID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM image_aliases WHERE image_id = ? ORDER BY name`, imageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (r *Registry) SetAlias(ctx context.Context, name, imageID string) error {
	if !aliasPattern.MatchString(name) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidAlias, name, aliasPattern)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO image_aliases (name, image_id, updated_at) SELECT ?, id, ? FROM images WHERE id = ? AND removed_at IS NULL
		ON CONFLICT (name) DO UPDATE SET image_id = excluded.image_id, updated_at = excluded.updated_at`,
		name, time.Now().UTC().Format(time.RFC3339Nano), imageID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: %s", ErrNotFound, imageID)
	}
	return nil
}

func (r *Registry) List(ctx context.Context) ([]Image, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, digest, status, created_at FROM images WHERE removed_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	list := []Image{}
	for rows.Next() {
		var img Image
		if err := rows.Scan(&img.ID, &img.Digest, &img.Status, &img.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, img)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Aliases, err = r.aliases(ctx, list[i].ID); err != nil {
			return nil, err
		}
	}
	return list, nil
}
