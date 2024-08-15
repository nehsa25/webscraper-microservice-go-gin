
write-host "Building docker image for web scraper microservice";
docker build . -t nehsa/web-scraper:latest --platform linux/amd64;

write-host "Pushing image to DockerHub...";
docker push nehsa/web-scraper:latest;

write-host "Done!";