# Neutral bot-detection test pages, one visit each per configuration.
PAGES = [
    ("sannysoft", "https://bot.sannysoft.com/", 8),
    ("creepjs", "https://abrahamjuliot.github.io/creepjs/", 25),
    ("browserscan", "https://www.browserscan.net/bot-detection", 15),
    ("dbi", "https://deviceandbrowserinfo.com/are_you_a_bot", 12),
    ("cfchallenge", "https://www.scrapingcourse.com/cloudflare-challenge", 25),
]

EXTRACT = r"""() => {
  const out = {title: document.title, url: location.href,
    webdriver: navigator.webdriver, ua: navigator.userAgent,
    text: (document.body ? document.body.innerText : '').slice(0, 7000)};
  const failed = [...document.querySelectorAll('td.failed, td.warn')].map(td => {
    const row = td.closest('tr'); return (row ? row.innerText : td.innerText).replace(/\s+/g,' ').slice(0,120) + ' [' + td.className + ']';
  });
  out.sanny_failed = failed;
  return JSON.stringify(out);
}"""
