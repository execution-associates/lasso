cd ~/dl/cf && CAMOFOX_CRASH_REPORT_ENABLED=false npm i @askjo/camofox-browser@1.18.1 2>&1 | tail -3
P=node_modules/@askjo/camofox-browser; ls $P
grep -rhoE "process\.env\.[A-Z_]+" $P/lib/config.js $P/server.js 2>/dev/null | sort -u | tr "\n" " "; echo
grep -A6 '"bin"' $P/package.json; grep -E '"(main|start)"' $P/package.json
