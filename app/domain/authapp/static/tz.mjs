// The device's time zone, for the weekly email (docs/accountant.md, 5).
//
// The sign-in forms carry a hidden field this fills, which the server
// keeps only when the person has no time zone yet; and the profile's time
// zone, when empty, gets an offer of the device's, which fills the field
// when taken and saves nothing until the form is saved. Without script,
// the zone is chosen by hand, and until it is the weekly email's hours are
// UTC, which the profile says.
//
// The words are the page's, translated there: the offer carries its
// sentence with {zone} for the name.

// zone is the device's, or "" when the browser will not say.
export function deviceZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

// fill fills the hidden fields and makes the offers, in doc.
export function fill(doc, zone) {
  if (!zone) return;

  for (const input of doc.querySelectorAll("input[data-device-zone]")) {
    if (!input.value) input.value = zone;
  }

  for (const offer of doc.querySelectorAll("[data-zone-offer]")) {
    const field = doc.getElementById(offer.dataset.zoneOffer);
    if (!field || field.value) continue;

    offer.querySelector("span").textContent = offer.dataset.say.replace("{zone}", zone);
    offer.querySelector("button").addEventListener("click", () => {
      field.value = zone;
      offer.hidden = true;
      field.focus();
    });
    offer.hidden = false;
  }
}

if (typeof document !== "undefined") fill(document, deviceZone());
