// Package backup is the import/export page's half of the server: guard's
// configuration as one file, and back again.
//
// Every endpoint here is `admin`, reads included. The summary is only counts,
// but the export is the whole configuration — including, when a passphrase is
// given, every credential guard holds — so the section is as privileged as the
// most privileged thing in it.
//
// What travels and what does not is decided in internal/telemetry/backup.go,
// beside the database it reads. Nothing here knows a table name; the request
// shapes live in server/apis/contract, like every other one.
package backup
