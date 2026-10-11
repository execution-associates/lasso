# Sounds

The inbox's arrival sounds, played when something new lands in it (Settings › Sound picks one, or none). Each was generated with ElevenLabs' sound-effects model (`fal-ai/elevenlabs/sound-effects/v2` on fal), then levelled to a -4 dBFS peak with its leading silence trimmed (`ffmpeg … silenceremove,volume`), so they are equally loud and sound at once.

| File | Prompt |
|---|---|
| `whoosh.mp3` | Soft airy whoosh passing by, smooth swish of air, clean UI notification for a new message arriving, no impact, no music (0.8 s) |
| `marimba.mp3` | Single soft wooden marimba pluck, round and mellow, subtle desktop app message notification, short and dry (0.7 s) |
| `lasso.mp3` | Gentle lasso rope whoosh ending in a soft muted bell tap, short western-tinged notification sound for an inbox message, subtle and clean (1.2 s) |

`build-app.sh` copies this folder into the bundle's `Contents/Resources/Sounds`.
