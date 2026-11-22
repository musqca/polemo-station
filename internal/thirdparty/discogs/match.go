package discogs

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type release struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Artists   []artist `json:"artists"`
	Tracklist []track  `json:"tracklist"`
}

type track struct {
	Title    string   `json:"title"`
	Type     string   `json:"type_"`
	Duration string   `json:"duration"`
	Artists  []artist `json:"artists"`
}

type artist struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	ANV  string `json:"anv"`
	Join string `json:"join"`
}

type trackMatch struct {
	Result
	albumMatch   bool
	artistRank   int
	durationDiff time.Duration
	leadID       int64
	leadName     string
}

type matchQuery Query

func normalizedQuery(input Query) matchQuery {
	return matchQuery{
		Title:       normalizeText(input.Title),
		Artist:      normalizeArtist(input.Artist),
		Album:       normalizeText(input.Album),
		AlbumArtist: normalizeArtist(input.AlbumArtist),
		Duration:    input.Duration,
	}
}

func (q matchQuery) releasePriority(summary string) int {
	name, album, ok := strings.Cut(summary, " - ")
	if !ok {
		return 0
	}
	name = normalizeArtist(artistSuffix.ReplaceAllString(strings.TrimSpace(name), ""))
	priority := 0
	if q.Album != "" && normalizeText(album) == q.Album {
		priority += 4
	}
	if name != "" && name == q.AlbumArtist {
		priority += 2
	}
	if name != "" && name == q.Artist {
		priority++
	}
	return priority
}

func (q matchQuery) match(release release, releaseURL string, alternatives bool) []trackMatch {
	album := strings.TrimSpace(release.Title)
	albumMatch := q.Album != "" && normalizeText(album) == q.Album
	if !alternatives && q.Artist == "" && (!albumMatch || (q.AlbumArtist != "" && creditRank(q.AlbumArtist, release.Artists) == 0)) {
		return nil
	}
	var matches []trackMatch
	for _, track := range release.Tracklist {
		if (track.Type != "" && track.Type != "track") || normalizeText(track.Title) != q.Title {
			continue
		}
		artists := track.Artists
		if len(artists) == 0 {
			artists = release.Artists
		}
		name := artistCredit(artists, true)
		if nonSpecificArtist(normalizeArtist(name)) || nonSpecificArtist(normalizeArtist(artistCredit(artists, false))) {
			continue
		}
		rank := creditRank(q.Artist, artists)
		if !alternatives && q.Artist != "" && rank == 0 {
			continue
		}
		diff := time.Duration(-1)
		if duration := trackDuration(track.Duration); q.Duration > 0 && duration > 0 {
			diff = max(q.Duration-duration, duration-q.Duration)
			tolerance := min(30*time.Second, max(12*time.Second, q.Duration/10))
			if !alternatives && diff > tolerance {
				continue
			}
		}
		matches = append(matches, trackMatch{
			Result: Result{
				Title: strings.TrimSpace(track.Title), Artist: name, Album: album, ReleaseURL: releaseURL,
			},
			albumMatch: albumMatch, artistRank: rank, durationDiff: diff,
			leadID: artists[0].ID, leadName: normalizeArtist(artists[0].Name),
		})
	}
	return matches
}

func (q matchQuery) bestMatch(matches []trackMatch) (Result, error) {
	if len(matches) == 0 {
		return Result{}, ErrNoMatch
	}
	sortMatches(matches)
	best := matches[0]
	for _, other := range matches[1:] {
		if best.albumMatch == other.albumMatch && differentArtists(best, other) &&
			(best.durationDiff < 0 || other.durationDiff < 0 || other.durationDiff-best.durationDiff < 4*time.Second) {
			return Result{}, ErrNoMatch
		}
	}
	if q.Album != "" && !best.albumMatch {
		best.Album = ""
	}
	if q.Album == "" {
		for _, other := range matches[1:] {
			if normalizeText(best.Album) != normalizeText(other.Album) {
				best.Album = ""
				break
			}
		}
	}
	return best.Result, nil
}

func sortMatches(matches []trackMatch) {
	slices.SortStableFunc(matches, func(a, b trackMatch) int {
		if a.albumMatch != b.albumMatch {
			if a.albumMatch {
				return -1
			}
			return 1
		}
		if rank := cmp.Compare(b.artistRank, a.artistRank); rank != 0 {
			return rank
		}
		return cmp.Compare(matchDistance(a), matchDistance(b))
	})
}

func differentArtists(a, b trackMatch) bool {
	if a.leadID > 0 && b.leadID > 0 {
		return a.leadID != b.leadID
	}
	return a.leadName != b.leadName
}

func matchDistance(match trackMatch) time.Duration {
	if match.durationDiff < 0 {
		return 1<<63 - 1
	}
	return match.durationDiff
}

var artistSuffix = regexp.MustCompile(`\s+\(([2-9]|[1-9][0-9]+)\)$`)

func (a artist) name(variation bool) string {
	if anv := strings.TrimSpace(a.ANV); variation && normalizeArtist(anv) != "" {
		return anv
	}
	return artistSuffix.ReplaceAllString(strings.TrimSpace(a.Name), "")
}

func artistCredit(artists []artist, variation bool) string {
	var credit strings.Builder
	var join string
	for _, artist := range artists {
		name := artist.name(variation)
		if name == "" {
			continue
		}
		if credit.Len() > 0 {
			if join == "" || join == "," {
				credit.WriteString(", ")
			} else {
				credit.WriteString(" " + join + " ")
			}
		}
		credit.WriteString(name)
		join = strings.TrimSpace(artist.Join)
	}
	return credit.String()
}

func creditRank(want string, artists []artist) int {
	if want == "" || len(artists) == 0 {
		return 0
	}
	for _, variation := range []bool{true, false} {
		if normalizeArtist(artistCredit(artists, variation)) == want {
			return 2
		}
	}
	lead := artists[0]
	if normalizeArtist(lead.name(true)) == want || normalizeArtist(lead.name(false)) == want ||
		normalizeArtist(lead.Name) == want {
		return 1
	}
	return 0
}

func nonSpecificArtist(value string) bool {
	switch value {
	case "", "various", "various artists", "unknown artist", "no artist":
		return true
	}
	return false
}

func normalizeText(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '\'' || r == '’' || r == '‘':
			return -1
		case unicode.IsPunct(r):
			return ' '
		default:
			return r
		}
	}, strings.ToLower(norm.NFKC.String(value)))
	return strings.Join(strings.Fields(value), " ")
}

var artistJoins = strings.NewReplacer(" feat ", " and ", " ft ", " and ", " featuring ", " and ")

func normalizeArtist(value string) string {
	// Discogs credits may use an ampersand, a join word, or "feat.".
	return artistJoins.Replace(normalizeText(strings.ReplaceAll(value, "&", " and ")))
}

func trackDuration(value string) time.Duration {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0
	}
	var seconds int64
	for i, part := range parts {
		number, err := strconv.ParseUint(part, 10, 16)
		if err != nil || (i > 0 && number >= 60) {
			return 0
		}
		seconds = seconds*60 + int64(number)
	}
	return time.Duration(seconds) * time.Second
}
