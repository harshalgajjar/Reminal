class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.7.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.1/reminal_1.7.1_darwin_arm64.tar.gz"
      sha256 "e7204f290943fc1861fca4a46643651df1a5cb1af523895c82fc365c83d58777"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.1/reminal_1.7.1_darwin_amd64.tar.gz"
      sha256 "0767980f44367a020457e76f85d31d429824d0c775d5c7a67d1de36c2527ea56"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.1/reminal_1.7.1_linux_arm64.tar.gz"
      sha256 "6576b33af857305b34d6e0eb1123583141ab3a331dbcdbb2b6606ba0102c7602"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.1/reminal_1.7.1_linux_amd64.tar.gz"
      sha256 "9ce2ae62673e7b55a2e654ee36b9abc966050d286c465e4468be8440718e6669"
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
