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
	"time"

	"github.com/onatm/boxface/internal/id"
)

var (
	ErrInvalidSource = errors.New("invalid import source")
	ErrInvalidAlias  = errors.New("invalid image alias")
	ErrNotFound      = errors.New("image not found")
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

	if existing, err := r.byDigest(ctx, sum); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
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
	res, err := r.db.ExecContext(ctx, `INSERT INTO image_aliases (name, image_id, updated_at) SELECT ?, id, ? FROM images WHERE id = ?
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
	rows, err := r.db.QueryContext(ctx, `SELECT id, digest, status, created_at FROM images ORDER BY created_at`)
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
