class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_darwin_arm64.tar.gz"
      sha256 "088fc29eefe0f66b706283f1b97fb888231dad89cc70a137eb3e9fc8cbe6539b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_darwin_amd64.tar.gz"
      sha256 "2aaf70f61530a6eb12d703b31eae0e6d063f1d072cf751f5b22b942116659877"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_linux_arm64.tar.gz"
      sha256 "2bf9adc129e09b611ffd3ebc2e1f74068c8f608f50b700e49039ad220dec1a29"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
