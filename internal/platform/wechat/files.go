package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// File holds a stable upload identity or the encrypted CDN reference returned by UploadFile.
// Store its key in trusted encrypted host storage. Data belongs to the caller.
type File struct {
	Name     string
	Data     []byte `json:",omitempty"`
	Size     int
	Key      string
	FileKey  string
	Download string `json:",omitempty"`
}

// NewFile copies a file of at most 8 MiB and generates identities for a single upload.
// Persist the returned value before uploading so retries retain the same keys.
func NewFile(name string, data []byte) (File, error) {
	if name == "" || len(name) > 256 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\r\n\x00") || len(data) > 8<<20 {
		return File{}, errors.New("wechat_file_invalid")
	}
	var key [16]byte
	var id [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return File{}, err
	}
	if _, err := rand.Read(id[:]); err != nil {
		return File{}, err
	}
	return File{Name: name, Data: bytes.Clone(data), Size: len(data), Key: hex.EncodeToString(key[:]), FileKey: hex.EncodeToString(id[:])}, nil
}

// UploadFile encrypts and uploads a bounded file with the supplied stable upload identity.
// It leaves the input unchanged and returns a durable reference for SendFile.
func (a *Plugin) UploadFile(ctx context.Context, c Credentials, file File) (File, error) {
	key, err := hex.DecodeString(file.Key)
	if err != nil || len(key) != 16 || len(file.Data) > 8<<20 {
		return File{}, errors.New("wechat_file_invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return File{}, errors.New("wechat_file_invalid")
	}
	padding := aes.BlockSize - len(file.Data)%aes.BlockSize
	encrypted := make([]byte, len(file.Data)+padding)
	copy(encrypted, file.Data)
	copy(encrypted[len(file.Data):], bytes.Repeat([]byte{byte(padding)}, padding))
	for offset := 0; offset < len(encrypted); offset += aes.BlockSize {
		block.Encrypt(encrypted[offset:offset+aes.BlockSize], encrypted[offset:offset+aes.BlockSize])
	}
	digest := md5.Sum(file.Data)
	var upload struct {
		Ret       int    `json:"ret"`
		Code      int    `json:"errcode"`
		URL       string `json:"upload_full_url"`
		Parameter string `json:"upload_param"`
	}
	err = a.request(ctx, c.BaseURL, "/ilink/bot/getuploadurl", c.Token, map[string]any{
		"filekey": file.FileKey, "media_type": 3, "to_user_id": c.PeerID,
		"rawsize": len(file.Data), "rawfilemd5": hex.EncodeToString(digest[:]), "filesize": len(encrypted),
		"no_need_thumb": true, "aeskey": file.Key, "base_info": baseInfo(),
	}, &upload)
	if upload.Ret == -14 || upload.Code == -14 {
		return File{}, errors.New("wechat_session_expired")
	}
	if err != nil {
		return File{}, err
	}
	if upload.Ret != 0 || upload.Code != 0 {
		return File{}, errors.New("wechat_file_upload_failed")
	}
	target := upload.URL
	if target == "" && upload.Parameter != "" && len(upload.Parameter) <= 32768 {
		target = "https://novac2c.cdn.weixin.qq.com/c2c/upload?" + url.Values{"encrypted_query_param": {upload.Parameter}, "filekey": {file.FileKey}}.Encode()
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host != "novac2c.cdn.weixin.qq.com" || u.User != nil || u.Fragment != "" || u.Path != "/c2c/upload" || len(target) > 65536 {
		return File{}, errors.New("wechat_file_upload_url_invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encrypted))
	if err != nil {
		return File{}, errors.New("wechat_file_upload_failed")
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := a.do(request)
	if err != nil {
		if ctx.Err() != nil {
			return File{}, ctx.Err()
		}
		return File{}, errors.New("wechat_file_upload_failed")
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	parameter := response.Header.Get("x-encrypted-param")
	if readErr != nil || closeErr != nil || response.StatusCode != 200 || parameter == "" || len(parameter) > 32768 || strings.ContainsAny(parameter, "\r\n\x00") {
		return File{}, errors.New("wechat_file_upload_failed")
	}
	file.Download, file.Size, file.Data = parameter, len(file.Data), nil
	return file, nil
}

// SendFile delivers one previously uploaded file to the currently bound peer.
// Persist its stable client identity before calling and retain uncertain outcomes.
func (a *Plugin) SendFile(ctx context.Context, c Credentials, id, contextToken string, file File) error {
	key, err := hex.DecodeString(file.Key)
	if err != nil || len(key) != 16 || file.Download == "" || len(file.Download) > 32768 || strings.ContainsAny(file.Download, "\r\n\x00") || file.Size < 0 || file.Size > 8<<20 || file.Name == "" || len(file.Name) > 256 || !utf8.ValidString(file.Name) || strings.ContainsAny(file.Name, "/\\\r\n\x00") {
		return errors.New("wechat_file_invalid")
	}
	return a.sendItems(ctx, c, id, contextToken, []any{map[string]any{"type": 4, "file_item": map[string]any{
		"file_name": file.Name, "len": strconv.Itoa(file.Size), "media": map[string]any{
			"encrypt_query_param": file.Download, "aes_key": base64.StdEncoding.EncodeToString([]byte(file.Key)), "encrypt_type": 1,
		},
	}}})
}
