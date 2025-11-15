var draw = function(hashrate, doarate, blocks, renderTo, interval) {
  var chartHashrate = document.getElementById("js-chart-hourly-hashrate");
  var labels = [];
  var hashrate_data = [];
  var lineChartHashrateData = {
    datasets: [{
      labels: labels,
      label: "Hashrate",
      fill: true,
      lineTension: 0,
      backgroundColor: 'rgba(52,165,68, 0.1)',
      borderWidth: 2,
      borderColor: "#34a544",
      borderCapStyle: 'butt',
      borderDash: [],
      borderDashOffset: 0.0,
      borderJoinStyle: 'miter',
      //pointStyle: 'cross',
      pointRadius: 4,
      pointBorderColor: "#2f9a3e",
      pointBackgroundColor: "#fff",
      pointBorderWidth: 2,
      pointHoverRadius: 6,
      pointHoverBackgroundColor: "#fff",
      pointHoverBorderColor: "#00AAFF",
      pointHoverBorderWidth: 2,
      //pointRadius: 4,
      //pointHitRadius: 5,
      data: hashrate_data,
      spanGaps: false
    }, {
      label: "Rejected",
      fill: true,
      lineTension: 0,
      backgroundColor: 'rgba(249,83,89, 0.1)',
      borderWidth: 2,
      borderColor: "#f95359",
      pointRadius: 4,
      pointBorderColor: "#d35847",
      pointBackgroundColor: "#fff",
      pointBorderWidth: 2,
      pointHoverRadius: 6,
      pointHoverBackgroundColor: "#fff",
      pointHoverBorderColor: "#886CE6",
      pointHoverBorderWidth: 2,
      data: doarate,
      spanGaps: false
    }]
  };

  if (chartHashrate) {
    var lineChart = new Chart(chartHashrate, {
      type: 'line',
      data: lineChartHashrateData,
      options: {
        legend: {
          display: true,
          labels: {
            fontColor: '#7F8FA4',
            fontFamily: '"Source Sans Pro", sans-serif',
            boxRadius: 4,
            usePointStyle: true
          }
        },
        layout: {
          padding: {
            left: 0,
            right: 0,
            top: 0,
            bottom: 0
          }
        },
        scales: {
          xAxes: [{
            display: true,
            time: {
              unit: 'minute'
            },
            ticks: {
              fontSize: '11',
              fontColor: '#969da5'
            },
            gridLines: {
              color: 'rgba(0,0,0,0.0)',
              zeroLineColor: 'rgba(0,0,0,0.0)'
            }
          }],
          yAxes: [{
            label: 'Hashrate',
            display: true,
            gridLines: {
              color: 'rgba(223,226,229,0.45)',
              zeroLineColor: 'rgba(0,0,0,0.0)'
              //tickMarkLength:
            },
            ticks: {
              beginAtZero: true,
              stepSize: 25,
              fontSize: '11',
              fontColor: '#969da5'
            }
          }]
        }
      }
    });
  }
};
