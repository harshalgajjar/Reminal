# The browser rig's image: Playwright's Chromium (it still takes
# --load-extension), with ffmpeg for the recording and a small window
# manager, so windows take focus as on a desktop.
FROM mcr.microsoft.com/playwright:v1.56.0-noble
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends ffmpeg openbox xdotool >/dev/null && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /e2e && cd /e2e && npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1
