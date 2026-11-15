# Game event code map

Game event code stays in the root `tytd_go` package because the engines share
private session state and websocket routing. Each event's implementation is
kept in an event-named Go file instead of one large catch-all source file.

## Shared event infrastructure

| Responsibility | Files |
| --- | --- |
| Event-account session, login, websocket reader and writer | `player_event.go` |
| Shared event map-stream plumbing | `player_event_mapstream.go`, `mapstream.go` |
| Main-account activity tracking and refresh | `player_activity.go` |

## Event engines

| Event | Event-account engine | Main-account engine / related modules |
| --- | --- | --- |
| God Island: Bồng Lai and Thiên Ma | `player_event_godisland.go` | `player_godisland.go`, `player_event_gidef.go`, `player_event_gipeople.go` |
| Đỉnh Bắc Minh | `player_event_bacminh.go` | `player_bacminh.go` |
| Thượng Cổ Di Tích (Ball GVG) | `player_event_ballgvg.go` | `player_ballgvg.go` |
| Cửu U (Holy Land) | `player_event_holyland.go` | `player_holyland.go` |
| Côn Lôn | `player_event_kunlunwar.go` | `player_kunlunwar.go` |
| Luân Hồi Bí Cảnh | `player_event_reborntrial.go` | `player_reborntrial.go`, `player_reborntrial_battle.go`, `player_reborntrial_energy.go` |
| Thương Minh Yêu Vực | `player_event_seawar.go` | `player_seawar.go`, `player_seawar_value.go` |

Tests follow the implementation name where possible: for example,
`player_event_holyland_test.go` tests the event-account engine and
`player_holyland_test.go` tests the main-account engine. Files ending in
`_live_test.go` require the repository's documented live-test tag and account
setup; ordinary refactors should use offline tests and builds.

## Naming rule for new or moved event code

- Put event-account protocol and automation in `player_event_<event>.go`.
- Put main-account protocol and automation in `player_<event>.go`.
- Keep shared login, socket lifecycle and cross-event dispatch in
  `player_event.go`; keep shared map streaming in `player_event_mapstream.go`.
- Put UI commands and handlers in the matching `telebot_<event>.go` and
  `web_<event>.go` files.
- Keep related tests named after their source engine. Do not merge all engines
  into one source file: that couples unrelated protocols and makes changes
  harder to review.
