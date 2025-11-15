// ==================================================================
// CONFIG
// ==================================================================
var api_url = "/api/stats";
var reload_interval = 5000; // 5 seconds
var currency_symbol = "VTC"; // You can change this if you fork for another coin

// ==================================================================
// MAIN DATA REFRESH FUNCTION
// ==================================================================
function refreshStats() {
    $.getJSON(api_url, function(data) {
        // 'data' is the full JSON object from our Go API

        // --- Update Top Toolbar ---
        $('#node_uptime').text(data.node_uptime);
        $('#block-diff').text(parseFloat(data.network_difficulty).toFixed(6)); // Uses ID from index.html
        $('#active-miners').text(data.connected_miners); // Uses ID from index.html
        $('#last-block-found').text(data.last_block_found_ago); // Uses ID from index.html
        
        if (data.connected_miners == 1) {
             $('#active-miners-label').text("Miner");
        } else {
             $('#active-miners-label').text("Miners");
        }

        // --- Update Node Statistics Table ---
        $('#node_fee').text(data.pool_fee); // Go API sends '1.0'
        $('#network_rate').text(data.global_network_hashrate);
        $('#global_rate').text(data.p2pool_network_hashrate);
        $('#local_rate').text(data.local_node_hashrate);
        var shares_text = 'Total: ' + data.pool_shares_total + 
                          ' (Orphan: ' + data.pool_shares_orphan +
                          ', Dead: ' + data.pool_shares_dead + ')';
        $('#shares').text(shares_text);
        $('#stats_blocks_found_daily').text(data.pool_blocks_found_24h);
        $('#block-reward').text(data.block_reward + ' ' + currency_symbol); // Uses ID from index.html
        $('#share_difficulty').text(data.min_share_difficulty);
        $('#expected_time_to_block').text(data.pool_time_to_block);
        $('#p2pool_version').text('p2pool-go-VTC'); // Hardcode our node name
        
        // --- Update Modal Popups & Header Info ---
        $('#modal_share_difficulty').text(data.min_share_difficulty);
        var stratumHost = window.location.hostname || "YOUR_IP_HERE";
        $('#stratum_host_info').text(stratumHost);
        $('#stratum_port_info').text(data.stratum_port);
        $('#stratum_url').text('stratum+tcp://' + stratumHost + ':' + data.stratum_port);
        $('#pool_title').text(stratumHost); // Set main title to the host


        // --- Update Active Miners Table ---
        var minersTable = $('#active_miners_table tbody');
        minersTable.empty(); // Clear old data
        if (data.active_miners && data.active_miners.length > 0) {
            $.each(data.active_miners, function(index, miner) {
                var tr = $('<tr/>').addClass('c-table__row');
                
                // [MODIFIED] Build the explorer URL based on the address prefix
                var isTestnet = miner.address.startsWith('tvtc1');
                var explorerBaseUrl = isTestnet ? 
                                    'https://chainz.cryptoid.info/vtc-test/address.dws?' : 
                                    'https://chainz.cryptoid.info/vtc/address.dws?';
                var explorerUrl = explorerBaseUrl + miner.address + '.htm';
                
                var addressLink = $('<a/>')
                                    .attr('href', explorerUrl)
                                    .attr('target', '_blank') // Opens in a new tab
                                    .text(miner.address);
                
                // [MODIFIED] Append the link instead of just text
                tr.append($('<td/>').addClass('c-table__cell').append(addressLink));

                tr.append($('<td/>').addClass('c-table__cell').text(miner.hashrate));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.rejected_percentage.toFixed(2) + '%'));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.share_difficulty.toFixed(3)));
                tr.append($('<td/>').addClass('c-table__cell').text(miner.avg_time_to_share));
                
                // Find this miner's payout in the payouts_list
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
        $('#num_blocks_found').text(data.blocks_found_24h);
        if (data.blocks_found_list && data.blocks_found_list.length > 0) {
            $.each(data.blocks_found_list, function(index, block) {
                var tr = $('<tr/>').addClass('c-table__row');
                tr.append($('<td/>').addClass('c-table__cell').text(block.block_number));
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
                
                // [MODIFIED] Build the explorer URL for the payout address
                var isTestnet = payout.address.startsWith('tvtc1');
                var explorerBaseUrl = isTestnet ? 
                                    'https://chainz.cryptoid.info/vtc-test/address.dws?' : 
                                    'https://chainz.cryptoid.info/vtc/address.dws?';
                var explorerUrl = explorerBaseUrl + payout.address + '.htm';
                
                var addressLink = $('<a/>')
                                    .attr('href', explorerUrl)
                                    .attr('target', '_blank') // Opens in a new tab
                                    .text(payout.address);
                
                // [MODIFIED] Append the link instead of just text
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
        // Clear tables on failure
        $('#active_miners_table tbody').empty().append('<tr class="c-table__row"><td colspan="7" class="c-table__cell">Error fetching stats...</td></tr>');
        $('#recent_blocks_table tbody').empty().append('<tr class-="c-table__row"><td colspan="2" class="c-table__cell">Error fetching stats...</td></tr>');
        $('#current_payouts_table tbody').empty().append('<tr class="c-table__row"><td colspan="2" class="c-table__cell">Error fetching stats...</td></tr>');
    });
}

// ==================================================================
// STARTUP
// ==================================================================
$(document).ready(function() {
    // Initial fetch
    refreshStats();
    
    // Set interval to refresh data
    setInterval(refreshStats, reload_interval);
    
    // We don't have graph data yet, so we'll just hide the graph buttons
    // You can re-enable this later if you implement graph history in the Go API
    $('#hour.hashrate').hide();
    $('#day.hashrate').hide();
    $('#week.hashrate').hide();
    $('#month.hashrate').hide();
    $('#year.hashrate').hide();
    
    // [REMOVED] All old init/update logic
});
