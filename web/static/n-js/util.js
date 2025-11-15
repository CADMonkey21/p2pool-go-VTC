// ======================================================================
// formats a given hashrate (H->H/s) to humand readable hashrate
// =Example:
// 1000 -> 1.00 KH/s
// ======================================================================
var formatHashrate = function(rate) {
    // [MODIFIED] Our Go API already sends a pre-formatted string like "1.28 MH/s".
    // This function is now just a pass-through.
    return rate;
}

// ======================================================================
// formats a given int value to human readable si format
// ======================================================================
var formatInt = function(rate) {
    // [MODIFIED] Pass-through.
    return rate;
}

// ======================================================================
// format seconds to an interval like '1d 7h 5s'
// ======================================================================
String.prototype.formatSeconds = function () {
    // [MODIFIED] Our Go API already sends a pre-formatted string like "22.5 minutes".
    // This function is now just a pass-through.
    // The 'this' value is the string from the API.
    return this.toString();
}

String.prototype.formatUptime = function () {
    // [MODIFIED] Our Go API already sends a pre-formatted string.
    return this.toString();
}

// ======================================================================
// Sorts a dict by value
// ======================================================================
function sortByValue(toSort) {
    // [MODIFIED] This is no longer needed, as our Go API sends pre-sorted lists.
    // However, we'll keep it here just in case, as the old p2pool.js might call it.
    var keys = Object.keys(toSort);
    keys.sort(function(a, b) {
        return toSort[a] < toSort[b] ? -1 : (toSort[a] === toSort[b] ? 0 : 1);
    });

    return keys;
}

// ======================================================================
// Creates a span with a badge for known addresses
// ======================================================================
function createAddressBadge(address) {
  // [MODIFIED] This is all hardcoded and specific to another pool.
  // We'll just return an empty string to keep it generic.
  return '';
}

// ======================================================================
// Creates a warning label for hashers that are on the wrong network
// ======================================================================
function createSpeedWarningLabel(type) {
  return ''; // This line returns an empty string, removing the exclamation triangle.
}
