(function () {
  "use strict";
  var fleet = document.getElementById("fleet");
  var count = document.getElementById("robot-count");
  var refresh = document.getElementById("refresh");
  var template = document.getElementById("robot-card");
  var activityFeed = document.getElementById("activity-feed");
  var activityTemplate = document.getElementById("activity-item");

  function profileName(esn) { return "vector-" + String(esn || "unknown").toLowerCase(); }
  function nameFor(esn) { return "Vector " + String(esn || "unknown").toUpperCase(); }
  function batteryPercent(volts) { return Math.max(0, Math.min(100, Math.round(((Number(volts) - 3.5) / .6) * 100))); }
  function showMessage(message, className) { fleet.innerHTML = ""; var node = document.createElement("article"); node.className = className || "empty"; node.textContent = message; fleet.appendChild(node); }
  function request(url) { return fetch(url, { cache: "no-store" }).then(function (response) { if (!response.ok) throw new Error("unavailable"); return response.json(); }); }
  function renderRobot(robot) {
    var card = template.content.firstElementChild.cloneNode(true);
    var esn = String(robot.esn || "");
    card.querySelector(".profile").textContent = profileName(esn) + " / isolated agent";
    card.querySelector("h3").textContent = nameFor(esn);
    card.querySelector(".connection").textContent = robot.activated ? "Enrolled · checking live SDK" : "Enrollment incomplete";
    card.querySelector(".operator").href = "/sdkapp/settings.html?serial=" + encodeURIComponent(esn);
    request("/api-sdk/get_battery?serial=" + encodeURIComponent(esn)).then(function (battery) {
      var line = card.querySelector(".battery"); var dot = line.querySelector("i");
      dot.className = "ok";
      card.querySelector(".connection").textContent = "Enrolled · live SDK responding";
      var charger = battery.is_on_charger_platform ? " · on charger" : "";
      line.querySelector("span").textContent = "~" + batteryPercent(battery.battery_volts) + "% · " + Number(battery.battery_volts).toFixed(2) + "V" + charger;
    }).catch(function () { card.querySelector(".battery span").textContent = "Battery unavailable"; });
    fleet.appendChild(card);
  }
  function formatTime(value) {
    var time = new Date(value);
    if (isNaN(time.getTime())) return "just now";
    return time.toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" });
  }
  function directionLabel(value) { return value === "hermes_to_wirepod" ? "HERMES → WIREPOD" : "WIREPOD → HERMES"; }
  function renderActivity(entries) {
    activityFeed.innerHTML = "";
    if (!entries.length) {
      var empty = document.createElement("li"); empty.className = "activity-empty"; empty.textContent = "No bridge activity yet. Ask a Vector something, or wait for an event."; activityFeed.appendChild(empty); return;
    }
    entries.forEach(function (entry) {
      var item = activityTemplate.content.firstElementChild.cloneNode(true);
      item.querySelector(".activity-direction").textContent = directionLabel(entry.direction);
      item.querySelector(".activity-direction").classList.add(entry.direction === "hermes_to_wirepod" ? "from-hermes" : "to-hermes");
      item.querySelector(".activity-kind").textContent = String(entry.kind || "bridge activity").replace(/_/g, " ");
      item.querySelector(".activity-detail").textContent = entry.detail ? " · " + entry.detail : "";
      var meta = (entry.esn ? "Vector " + String(entry.esn).toUpperCase() + " · " : "") + formatTime(entry.observed_at);
      if (entry.duration_ms) meta += " · " + Number(entry.duration_ms) + " ms";
      item.querySelector(".activity-meta").textContent = meta;
      item.querySelector(".activity-outcome").textContent = entry.outcome || "recorded";
      item.querySelector(".activity-outcome").classList.add("outcome-" + String(entry.outcome || "recorded").replace(/[^a-z]/g, ""));
      activityFeed.appendChild(item);
    });
  }
  function loadActivity() {
    request("/api-sdk/hermes_activity?limit=60").then(function (activity) {
      renderActivity(Array.isArray(activity.entries) ? activity.entries : []);
    }).catch(function () {
      activityFeed.innerHTML = ""; var unavailable = document.createElement("li"); unavailable.className = "activity-empty"; unavailable.textContent = "Bridge trace is temporarily unavailable."; activityFeed.appendChild(unavailable);
    });
  }
  function load() {
    refresh.disabled = true; refresh.textContent = "Refreshing…";
    request("/api-sdk/get_sdk_info").then(function (info) {
      var robots = Array.isArray(info.robots) ? info.robots : [];
      count.textContent = robots.length;
      fleet.innerHTML = "";
      if (!robots.length) { showMessage("No enrolled Vector bodies were found."); return; }
      robots.forEach(renderRobot);
    }).catch(function () { count.textContent = "0"; showMessage("WirePod could not read the enrolled fleet. Check that this host has finished setup."); })
      .finally(function () { refresh.disabled = false; refresh.textContent = "Refresh fleet"; });
    loadActivity();
  }
  refresh.addEventListener("click", load); load(); window.setInterval(loadActivity, 5000);
}());
