# future.md

# OpenSubsonic Compatibility

> Status: Future / Optional Compatibility Layer
> Priority: Low until core player and Media Center are stable

## 0. Why this exists

Our core product is **our own music player ecosystem**.

The official iOS / Android apps remain first-class clients and continue using our private APIs.

OpenSubsonic support exists for one purpose:

> Reuse the existing Subsonic / OpenSubsonic client ecosystem with minimal additional product complexity.

OpenSubsonic must remain an **adapter layer**, not the architecture of the product.

Do not redesign our internal services around OpenSubsonic.

Do not delay core player work in order to improve OpenSubsonic compatibility.

---

# 1. Product boundaries

## Core product

Our official apps own the advanced experience:

* Local music playback
* MPV playback engine
* Device music inventory
* Device-to-device file synchronization
* Media Center synchronization
* Metadata editing
* Lyrics editing
* Cover artwork editing
* Metadata / artwork / lyrics push to devices
* Playback state reporting
* Device awareness
* Future playback transfer
* Future playback session synchronization
* Other proprietary device features

These features belong to our private protocol.

They do NOT need to be represented in OpenSubsonic.

---

## OpenSubsonic role

OpenSubsonic is only a compatibility interface:

```text
Third-party Client
        │
        │ OpenSubsonic
        ▼
OpenSubsonic Adapter
        │
        ▼
Existing Internal Services
        │
        ├── MusicService
        ├── MetadataService
        ├── PlaybackService
        ├── PlaylistService
        └── SearchService
```

The adapter translates OpenSubsonic requests into existing internal service calls.

It must not contain core business logic.

---

# 2. Architecture rule

Internal services must not know that OpenSubsonic exists.

Preferred architecture:

```text
                    Official App
                         │
                    Private API
                         │
                         ▼
                  ┌──────────────┐
                  │ Core Services│
                  │              │
                  │ Music        │
                  │ Metadata     │
                  │ Playback     │
                  │ Search       │
                  │ Playlist     │
                  └──────┬───────┘
                         │
              ┌──────────┴──────────┐
              │                     │
           Web API          OpenSubsonic Adapter
              │                     │
           Svelte           Third-party Clients
```

Bad architecture:

```text
OpenSubsonic Handler
        │
        ├── query database directly
        ├── manipulate files directly
        ├── launch ffmpeg directly
        ├── update metadata directly
        └── contain product business logic
```

Good architecture:

```text
OpenSubsonic Handler
        │
        ▼
PlaybackService.Stream(...)
MusicService.GetAlbum(...)
SearchService.Search(...)
```

---

# 3. Preconditions

Do NOT start OpenSubsonic implementation until most of these are true:

* [ ] Official player core experience is stable
* [ ] Media Center library model is stable
* [ ] Media ID model is stable
* [ ] Metadata model is stable
* [ ] Album / Artist / Track relationships are stable
* [ ] Cover artwork endpoint exists
* [ ] Lyrics endpoint exists
* [ ] Search works reliably
* [ ] Direct streaming works
* [ ] Browser playback works
* [ ] FFmpeg fallback transcoding works
* [ ] Authentication model is stable
* [ ] Internal services are separated from HTTP handlers

If implementing OpenSubsonic requires rewriting core services, stop.

Fix the internal abstraction first.

---

# 4. Phase 0 — Prepare without implementing

Goal:

Make future OpenSubsonic support cheap without actually building it.

## Tasks

* [ ] Keep Media IDs stable
* [ ] Keep HTTP layer separate from business services
* [ ] Create `MusicService`
* [ ] Create `PlaybackService`
* [ ] Create `MetadataService`
* [ ] Create `SearchService`
* [ ] Create `PlaylistService` only when needed
* [ ] Avoid returning database models directly from services
* [ ] Define transport-independent DTOs
* [ ] Keep transcoding behind `PlaybackService`

Possible layout:

```text
internal/

  music/
    service.go
    repository.go
    models.go

  metadata/
    service.go

  playback/
    service.go
    resolver.go
    direct.go
    transcode.go

  search/
    service.go

  playlist/
    service.go

  transport/

    privateapi/

    webapi/

    opensubsonic/
```

No OpenSubsonic code is required during this phase.

---

# 5. Phase 1 — Minimal compatibility

Goal:

A third-party OpenSubsonic client can:

> connect → browse → search → open album → play music

Nothing more.

This is the first useful milestone.

## Authentication

Implement only what is required by commonly used clients.

* [ ] ping
* [ ] authentication
* [ ] API version reporting

## Library browsing

* [ ] music folders
* [ ] artists
* [ ] artist details
* [ ] albums
* [ ] album details
* [ ] song details

## Search

* [ ] search artists
* [ ] search albums
* [ ] search tracks

Reuse:

```text
SearchService
```

Do not implement a separate Subsonic search engine.

## Streaming

* [ ] stream
* [ ] download
* [ ] cover art

Streaming must reuse:

```text
PlaybackService
```

Flow:

```text
OpenSubsonic /stream
        │
        ▼
PlaybackService.Resolve()
        │
        ├── Direct Play
        │
        └── Transcode
                │
                ▼
              FFmpeg
```

No duplicated transcoding implementation.

---

# 6. Definition of Phase 1 success

Phase 1 is done when several mainstream OpenSubsonic-compatible clients can:

1. Add the server
2. Authenticate
3. Browse artists
4. Browse albums
5. Search music
6. View cover artwork
7. Start playback
8. Seek
9. Play the next track
10. Download the original file where supported

At this point:

STOP.

Do not immediately continue implementing the entire protocol.

Use real demand to decide whether Phase 2 is necessary.

---

# 7. Phase 2 — Good music-client compatibility

Only start this phase if:

* Users are actually connecting third-party clients
* Compatibility becomes a meaningful acquisition feature
* Support requests indicate missing functionality
* Maintenance cost remains reasonable

Potential additions:

## Lyrics

* [ ] lyrics
* [ ] synchronized lyrics if supported by our model

Lyrics remain sourced from our existing metadata system.

```text
OpenSubsonic
     │
     ▼
MetadataService.GetLyrics()
```

## Favorites

* [ ] star track
* [ ] star album
* [ ] star artist
* [ ] unstar

Only implement if favorites already exist internally.

Do NOT create a second favorites system just for OpenSubsonic.

## Playlists

* [ ] list playlists
* [ ] playlist details
* [ ] create playlist
* [ ] update playlist
* [ ] delete playlist

Again:

```text
OpenSubsonic
      ↓
PlaylistService
```

## Playback reporting

* [ ] scrobble
* [ ] play count updates
* [ ] last played
* [ ] optional rating support

Map these onto our existing playback history model.

---

# 8. Phase 3 — Compatibility improvements

Only implement based on evidence.

Candidates:

* [ ] genres
* [ ] starred content
* [ ] recently played
* [ ] most played
* [ ] recently added
* [ ] random songs
* [ ] album lists
* [ ] similar songs
* [ ] top songs
* [ ] bookmarks
* [ ] saved play queue

These features are optional.

None are required for the core product.

---

# 9. Explicitly NOT planned

Do not implement these just because they exist in the protocol:

* Podcasts
* Internet radio
* Jukebox
* Server audio output
* DLNA
* Chromecast
* Multi-room audio
* Last.fm-specific infrastructure
* Plugin framework
* Podcast downloader
* Generic media server functionality
* Video support purely for OpenSubsonic completeness

Exception:

A feature can move into scope if our own product independently needs it.

The rule is:

> Product requirement first. Protocol compatibility second.

Never:

> Protocol contains feature → therefore product must implement feature.

---

# 10. Playback behavior

Our playback system supports three conceptual modes.

```text
Direct Play
Direct Stream
Transcode
```

## Direct Play

Original media can be consumed directly.

Examples:

```text
MP3
AAC
FLAC
Opus
```

depending on client capabilities.

## Direct Stream

Original audio can be reused while changing transport/container if needed.

Optional optimization.

Not required in the first implementation.

## Transcode

Unsupported source:

```text
APE
WMA
DSF
DFF
other unsupported formats
```

Flow:

```text
source
  │
  ▼
FFmpeg
  │
  ├── Opus
  └── AAC
```

Transcoding belongs exclusively to `PlaybackService`.

---

# 11. Client capability model

Do not hard-code playback behavior around OpenSubsonic.

Playback capability belongs to the general device/client model.

Example:

```go
type PlaybackCapabilities struct {
    DirectPlayFormats []string
    PreferredCodec    string
    MaxBitrate        int
}
```

Resolver:

```go
ResolvePlayback(
    media,
    capabilities,
)
```

Result:

```go
type PlaybackDecision struct {
    Mode       PlaybackMode
    Codec      string
    Bitrate    int
}
```

This must be reusable by:

```text
Official iOS App
Official Android App
Web Player
OpenSubsonic Client
Future Desktop Client
Future TV Client
```

---

# 12. Media identity

OpenSubsonic IDs must map to our own persistent media IDs.

Never expose filesystem paths as IDs.

Bad:

```text
/storage/music/artist/song.flac
```

Good:

```text
track_id = 01J...
```

Conceptually:

```text
Media
  │
  ├── Server copy
  ├── iPhone copy
  ├── Android copy
  └── future remote copy
```

OpenSubsonic references the logical Media entity.

It does not own physical file identity.

---

# 13. Keep official apps special

Third-party OpenSubsonic clients get:

```text
Browse
Search
Stream
Download
Cover
Lyrics
Playlist
Favorites
Playback history
```

Our official clients additionally get:

```text
Device file inventory
File synchronization
Metadata push
Lyrics push
Cover push
Device presence
Current playback status
Remote control
Playback transfer
Local-file preference
Cross-device playback session
Device-specific state
```

This difference is intentional.

OpenSubsonic compatibility should expand the ecosystem without commoditizing our official client experience.

---

# 14. Future PlaybackSession

Not part of OpenSubsonic.

Possible internal model:

```go
type PlaybackSession struct {
    ID             string
    MediaID        string
    DeviceID       string
    PositionMS     int64
    State          PlaybackState
    Queue          []string
    UpdatedAt      time.Time
}
```

Potential future features:

```text
Continue on another device

Play on device

Move playback

Remote pause

Remote next / previous

Shared queue

Web → iPhone

Android → Web
```

This remains private API functionality.

Do not attempt to force this model into OpenSubsonic.

---

# 15. Testing strategy

Compatibility should be tested against real clients, not only protocol documentation.

Maintain a small compatibility matrix:

```text
Client              Login   Browse   Search   Play   Lyrics
-------------------------------------------------------------
Client A              ✅       ✅       ✅      ✅      ✅
Client B              ✅       ✅       ✅      ✅      ⚠️
Client C              ✅       ✅       ✅      ✅      ❌
```

Do not chase 100% compatibility with every client.

Target:

> Works well with several popular clients.

Not:

> Perfect emulation of Navidrome.

---

# 16. Implementation principle

Every OpenSubsonic endpoint must answer one question:

> Can this endpoint be implemented by adapting an existing internal service?

If YES:

Implement it.

If NO:

Ask why.

If implementing the endpoint requires creating a major new subsystem purely for compatibility:

Do not implement it.

---

# 17. Scope guardrails

Before starting any new OpenSubsonic feature, ask:

### Does our official product need this?

If yes, implement the underlying feature properly and expose it through both APIs where appropriate.

### Do real third-party users need this?

If yes, consider compatibility work.

### Is this only being implemented because Navidrome supports it?

If yes:

STOP.

---

# 18. Priority order

The project priority remains:

```text
1. Official music player quality

2. Playback reliability

3. Local library experience

4. Device synchronization

5. Media Center metadata management

6. Media Center Web playback

7. Device playback coordination

8. OpenSubsonic minimal adapter

9. OpenSubsonic compatibility improvements

10. Everything else
```

If OpenSubsonic work begins moving above items 1–6:

Re-evaluate immediately.

---

# 19. First future implementation checklist

When it is finally time to implement OpenSubsonic:

```text
[ ] Read current OpenSubsonic specification

[ ] Pick 3–5 target third-party clients

[ ] Capture actual API calls made by those clients

[ ] Implement authentication + ping

[ ] Implement library browsing

[ ] Implement search

[ ] Implement cover artwork

[ ] Map /stream to PlaybackService

[ ] Map /download to original media

[ ] Test seeking and range behavior

[ ] Test transcoding

[ ] Test large libraries

[ ] Test UTF-8 / CJK metadata

[ ] Test multi-disc albums

[ ] Test compilation albums

[ ] Test embedded artwork

[ ] Test lyrics

[ ] Stop and ship
```

The final item is important:

> **Stop and ship.**

Do not continue implementing protocol endpoints simply because they exist.

---

# 20. Long-term strategic value

OpenSubsonic is not the product.

It is distribution leverage.

Our investment:

```text
OpenSubsonic Adapter
```

Potential return:

```text
Existing iOS clients
Existing Android clients
Desktop clients
TV clients
Car clients
Power-user ecosystem
```

The strategic goal is:

> Let other people maintain alternative clients while we focus on making our own product exceptional.

Our moat should remain:

```text
Great official player
        +
Device synchronization
        +
Media identity
        +
Metadata management
        +
Cross-device intelligence
```

OpenSubsonic simply opens another door into that library.

---

# Reminder

If this file starts growing faster than the player:

Close this file.

Go improve the player.

😂
