fast.com CLI "written" in Go<br><br>
AI generated, WIP<br>
DL/UL results are accurate, however since it uses an HTTPS request, ping result seems much higher. ( 1ms->15ms, 60ms->280ms )

```
$ ./fastcli
Fetching server list...
Found 5 potential servers from API.
Pinging servers to select the best ones...

Selected servers for speed tests:
  - https://ipv4-c004-ist001-vodafonetr-isp.1.oca.nflxvideo.net/speedtest?c=tr&n=212238&v=217&e=1747243249&t=LOXOJJpYYBRblBAkz5GUk1VOIZ0sJWbZ5O5EyQ (Istanbul, TR) - Latency: 15ms
  - https://ipv4-c002-saw001-vodafonetr-isp.1.oca.nflxvideo.net/speedtest?c=tr&n=212238&v=248&e=1747243249&t=SHin9hoc0l_B6TyxFgcgjk7ORbZRVXRW5o1ITA (Istanbul, TR) - Latency: 16ms
  - https://ipv4-c003-adb001-vodafonetr-isp.1.oca.nflxvideo.net/speedtest?c=tr&n=212238&v=137&e=1747243249&t=WfiZfla00ixU_p7P1F2IBVcAcZSYx5n0k7xpkA (Izmir, TR) - Latency: 46ms

Performing download test...
Starting download from 3 server(s) for 15s, chunk size 26214400 bytes...

Performing upload test...
Starting upload to 3 server(s) for 15s, chunk size 10485760 bytes...

--- Speed Test Results ---
Average Ping to selected servers: 26ms
Download Speed: 7340.03 Mbps
Upload Speed: 3590.32 Mbps
```
