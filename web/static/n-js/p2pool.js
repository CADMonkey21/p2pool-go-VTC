// ==================================================================
// CONFIG
// ==================================================================
var api_url = "/api/stats";
var reload_interval = 5000; // 5 seconds
var currency_symbol = "VTC"; // You can change this if you fork for another coin
var explorer_provider = "auto"; // Options: "auto", "cryptoid", "vtcblocks", "bitinfocharts"
var active_mainnet_explorer = "cryptoid";

function checkExplorerHealth() {
    if (explorer_provider !== "auto") {
        active_mainnet_explorer = explorer_provider;
        return;
    }
    $.ajax({
        url: "https://chainz.cryptoid.info/vtc/api.dws?q=getblockcount",
        method: "GET",
        timeout: 3500,
        success: function(res) {
            if (res && !isNaN(parseInt(res, 10))) {
                active_mainnet_explorer = "cryptoid";
            } else {
                active_mainnet_explorer = "vtcblocks";
            }
        },
        error: function() {
            active_mainnet_explorer = "vtcblocks";
        }
    });
}
checkExplorerHealth();
setInterval(checkExplorerHealth, 300000);

function getAddressExplorerUrl(address) {
    if (address && address.startsWith("tvtc1")) {
        return "https://chainz.cryptoid.info/vtc-test/address.dws?" + address + ".htm";
    }
    var provider = (explorer_provider === "auto") ? active_mainnet_explorer : explorer_provider;
    if (provider === "vtcblocks") {
        return "https://vtcblocks.com/address/" + address;
    } else if (provider === "bitinfocharts") {
        return "https://bitinfocharts.com/vertcoin/address/" + address;
    }
    return "https://chainz.cryptoid.info/vtc/address.dws?" + address + ".htm";
}

function getBlockExplorerUrl(blockTarget, isTestnet) {
    if (isTestnet) {
        return "https://chainz.cryptoid.info/vtc-test/block.dws?" + blockTarget + ".htm";
    }
    var provider = (explorer_provider === "auto") ? active_mainnet_explorer : explorer_provider;
    if (provider === "vtcblocks") {
        return "https://vtcblocks.com/block/" + blockTarget;
    } else if (provider === "bitinfocharts") {
        return "https://bitinfocharts.com/vertcoin/block/" + blockTarget;
    }
    return "https://chainz.cryptoid.info/vtc/block.dws?" + blockTarget + ".htm";
}

// ==================================================================
// MAIN DATA REFRESH FUNCTION
// ==================================================================
function refreshStats() {
    $.getJSON(api_url, function(data) {
        // 'data' is the full JSON object from our Go API

        // --- Update Top Toolbar ---
        $('#node_uptime').text(data.node_uptime);
        $('#block-diff').text(parseFloat(parseFloat(data.network_difficulty).toFixed(2)));
        $('#active-miners').text(data.connected_miners);
        $('#last-block-found').text(data.last_block_found_ago);

        if (data.connected_miners == 1) {
             $('#active-miners-label').text("Miner");
        } else {
             $('#active-miners-label').text("Miners");
        }

        // --- Update Node Statistics Table ---
        $('#node_fee').text(data.pool_fee);
        $('#network_rate').text(data.global_network_hashrate);
        $('#global_rate').text(data.p2pool_network_hashrate);
        $('#local_rate').text(data.local_node_hashrate);
        var shares_text = 'Total: ' + data.pool_shares_total +
                          ' (Orphan: ' + data.pool_shares_orphan +
                          ', Dead: ' + data.pool_shares_dead + ')';
        $('#shares').text(shares_text);
        $('#stats_blocks_found_daily').text(data.pool_blocks_found_24h);
        $('#block-reward').text(parseFloat(parseFloat(data.block_reward).toFixed(4)) + ' ' + currency_symbol);
        $('#share_difficulty').text(data.min_share_difficulty);
        $('#expected_time_to_block').text(data.pool_time_to_block);
        $('#p2pool_version').text('p2pool-go-VTC');
        var pCount = (data.connected_peers !== undefined) ? data.connected_peers : 0;
        var pIn = (data.peers_inbound !== undefined) ? data.peers_inbound : 0;
        var pOut = (data.peers_outbound !== undefined) ? data.peers_outbound : 0;
        $('#peers_count').text(pCount + ' (' + pIn + ' in / ' + pOut + ' out)');
        var peersContainer = $('#connected_peers_list');
        peersContainer.empty();
        if (data.peer_list && data.peer_list.length > 0) {
            var dashPort = data.stratum_port || 9172;
            $.each(data.peer_list, function(i, peer) {
                if (i > 0) peersContainer.append(', ');
                var host = peer.replace(/:\d+$/, '');
                var link = $('<a/>')
                    .attr('href', 'http://' + host + ':' + dashPort)
                    .attr('target', '_blank')
                    .text(peer);
                peersContainer.append(link);
            });
        } else {
            peersContainer.text('None');
        }

        // --- Update Modal Popups & Header Info ---
        $('#modal_share_difficulty').text(data.min_share_difficulty);
        var stratumHost = window.location.hostname || "YOUR_IP_HERE";
        $('#stratum_host_info').text(stratumHost);
        $('#stratum_port_info').text(data.stratum_port);
        $('#stratum_url').text('stratum+tcp://' + stratumHost + ':' + data.stratum_port);
        $('#pool_title').text(stratumHost);

        // --- Update Active Miners Table ---
        var minersTable = $('#active_miners_table tbody');
        minersTable.empty();
        if (data.active_miners && data.active_miners.length > 0) {
            $.each(data.active_miners, function(index, miner) {
                var tr = $('<tr/>').addClass('c-table__row');

                var explorerUrl = getAddressExplorerUrl(miner.address);

                var addressLink = $('<a/>')
                                    .attr('href', explorerUrl)
                                    .attr('target', '_blank')
                                    .text(miner.address);

                tr.append($('<td/>').addClass('c-table__cell').append(addressLink));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.hashrate));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.rejected_percentage.toFixed(2) + '%'));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.share_difficulty.toFixed(3)));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.avg_time_to_share));

                var payout = 0;
                if(data.payouts_list) {
                    var payoutEntry = data.payouts_list.find(p => p.address === miner.address);
                    if (payoutEntry) {
                        payout = payoutEntry.payout_vtc;
                    }
                }
                tr.append($('<td/>').addClass('c-table__cell').text(payout.toFixed(8) + ' ' + currency_symbol));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.est_24_hour_payout_vtc.toFixed(8) + ' ' + currency_symbol));

                minersTable.append(tr);
            });
        } else {
            minersTable.append('<tr class="c-table__row"><td colspan="7" class="c-table__cell">No miners are here...it\'s pretty lonely</td></tr>');
        }

        // --- Update Recent Blocks Table ---
        var blocksTable = $('#recent_blocks_table tbody');
        blocksTable.empty();
        var blocksCount = (data.pool_blocks_found_24h !== undefined) ? data.pool_blocks_found_24h : (data.blocks_found_list ? data.blocks_found_list.length : 0);
        $('#num_blocks_found').text(blocksCount);
        if (data.blocks_found_list && data.blocks_found_list.length > 0) {
            var isTestnet = (data.payouts_list && data.payouts_list.length > 0 && data.payouts_list[0].address.startsWith('tvtc1')) ||
                            (data.active_miners && data.active_miners.length > 0 && data.active_miners[0].address.startsWith('tvtc1'));
            $.each(data.blocks_found_list, function(index, block) {
                var tr = $('<tr/>').addClass('c-table__row');
                var blockTarget = block.block_hash || block.hash || block.block_number;
                var blockLink = $('<a/>')
                                    .attr('href', getBlockExplorerUrl(blockTarget, isTestnet))
                                    .attr('target', '_blank')
                                    .text(block.block_number);
                tr.append($('<td/>').addClass('c-table__cell').append(blockLink));
                tr.append($('<td/>').addClass('c-table__cell').text(block.found_ago));
                blocksTable.append(tr);
            });
        } else {
            blocksTable.append('<tr class="c-table__row"><td colspan="2" class="c-table__cell">No blocks found!</td></tr>');
        }

        // --- Update Payouts Table ---
        var payoutsTable = $('#current_payouts_table tbody');
        payoutsTable.empty();
        if (data.payouts_list && data.payouts_list.length > 0) {
            $('#num_payouts').text(data.payouts_list.length);
            $.each(data.payouts_list, function(index, payout) {
                var tr = $('<tr/>').addClass('c-table__row');

                var explorerUrl = getAddressExplorerUrl(payout.address);

                var addressLink = $('<a/>')
                                    .attr('href', explorerUrl)
                                    .attr('target', '_blank')
                                    .text(payout.address);

                tr.append($('<td/>').addClass('c-table__cell').append(addressLink));
                tr.append($('<td/>').addClass('c-table__cell').text(payout.payout_vtc.toFixed(8)));
                payoutsTable.append(tr);
            });
        } else {
            $('#num_payouts').text(0);
            payoutsTable.append('<tr class="c-table__row"><td class="c-table__cell">No payouts!</td><td class="c-table__cell"></td></tr>');
        }

    }).fail(function() {
        console.error("Failed to fetch data from /api/stats");
        $('#active_miners_table tbody').empty().append('<tr class="c-table__row"><td colspan="7" class="c-table__cell">Error fetching stats...</td></tr>');
        $('#recent_blocks_table tbody').empty().append('<tr class="c-table__row"><td colspan="2" class="c-table__cell">Error fetching stats...</td></tr>');
        $('#current_payouts_table tbody').empty().append('<tr class="c-table__row"><td colspan="2" class="c-table__cell">Error fetching stats...</td></tr>');
    });
}

// ==================================================================
// STARTUP
// ==================================================================
$(document).ready(function() {
    refreshStats();
    setInterval(refreshStats, reload_interval);

    $('#hour.hashrate').hide();
    $('#day.hashrate').hide();
    $('#week.hashrate').hide();
    $('#month.hashrate').hide();
    $('#year.hashrate').hide();
});
